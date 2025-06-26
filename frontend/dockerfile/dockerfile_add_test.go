package dockerfile

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/containerd/continuity/fs/fstest"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/frontend/dockerui"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/util/testutil"
	"github.com/moby/buildkit/util/testutil/httpserver"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/stretchr/testify/require"
	"github.com/tonistiigi/fsutil"
)

func init() {
	allTests = append(allTests, integration.TestFuncs(
		testAddFromURL,
		testAddArchive,
		testAddChownArchive,
		testAddArchiveWildcard,
		testAddChownExpand,
		testAddURLChmod,
		testAddInvalidChmod,
	)...)
}

func testAddFromURL(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	f := getFrontend(t, sb)
	f.RequiresBuildctl(t)

	modTime := time.Now().Add(-24 * time.Hour) // avoid falso positive with current time

	resp := httpserver.Response{
		Etag:    identity.NewID(),
		Content: []byte("content1"),
	}

	resp2 := httpserver.Response{
		Etag:         identity.NewID(),
		LastModified: &modTime,
		Content:      []byte("content2"),
	}

	server := httpserver.NewTestServer(map[string]httpserver.Response{
		"/foo": resp,
		"/":    resp2,
	})
	defer server.Close()

	dockerfile := fmt.Appendf(nil, `
FROM scratch
ADD %s /dest/
`, server.URL+"/foo")

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	args, trace := f.DFCmdArgs(dir.Name, dir.Name)
	defer os.RemoveAll(trace)

	destDir := t.TempDir()

	cmd := sb.Cmd(args + fmt.Sprintf(" --output type=local,dest=%s", destDir))
	err := cmd.Run()
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "dest/foo"))
	require.NoError(t, err)
	require.Equal(t, []byte("content1"), dt)

	// run again to test HEAD request
	cmd = sb.Cmd(args)
	err = cmd.Run()
	require.NoError(t, err)

	// test the default properties
	dockerfile = fmt.Appendf(nil, `
FROM scratch
ADD %s /dest/
`, server.URL+"/")

	dir = integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	args, trace = f.DFCmdArgs(dir.Name, dir.Name)
	defer os.RemoveAll(trace)

	destDir = t.TempDir()

	cmd = sb.Cmd(args + fmt.Sprintf(" --output type=local,dest=%s", destDir))
	err = cmd.Run()
	require.NoError(t, err)

	destFile := filepath.Join(destDir, "dest/__unnamed__")
	dt, err = os.ReadFile(destFile)
	require.NoError(t, err)
	require.Equal(t, []byte("content2"), dt)

	fi, err := os.Stat(destFile)
	require.NoError(t, err)
	require.Equal(t, modTime.Format(http.TimeFormat), fi.ModTime().Format(http.TimeFormat))

	stats := server.Stats("/foo")
	require.Len(t, stats.Requests, 2)
	require.Equal(t, "GET", stats.Requests[0].Method)
	require.Contains(t, stats.Requests[0].Header.Get("User-Agent"), "buildkit/v")
	require.Equal(t, "HEAD", stats.Requests[1].Method)
	require.Contains(t, stats.Requests[1].Header.Get("User-Agent"), "buildkit/v")
}

func testAddArchive(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)
	f.RequiresBuildctl(t)

	buf := bytes.NewBuffer(nil)
	tw := tar.NewWriter(buf)
	expectedContent := []byte("content0")
	err := tw.WriteHeader(&tar.Header{
		Name:     "foo",
		Typeflag: tar.TypeReg,
		Size:     int64(len(expectedContent)),
		Mode:     0644,
	})
	require.NoError(t, err)
	_, err = tw.Write(expectedContent)
	require.NoError(t, err)
	err = tw.Close()
	require.NoError(t, err)

	baseImage := integration.UnixOrWindows("scratch", "nanoserver")

	dockerfile := fmt.Appendf(nil, `
FROM %s
ADD t.tar /
`, baseImage)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateFile("t.tar", buf.Bytes(), 0600),
	)

	args, trace := f.DFCmdArgs(dir.Name, dir.Name)
	defer os.RemoveAll(trace)

	destDir := t.TempDir()

	cmd := sb.Cmd(args + fmt.Sprintf(" --output type=local,dest=%s", destDir))
	require.NoError(t, cmd.Run())

	dt, err := os.ReadFile(filepath.Join(destDir, "foo"))
	require.NoError(t, err)
	require.Equal(t, expectedContent, dt)

	// add gzip tar
	buf2 := bytes.NewBuffer(nil)
	gz := gzip.NewWriter(buf2)
	_, err = gz.Write(buf.Bytes())
	require.NoError(t, err)
	err = gz.Close()
	require.NoError(t, err)

	dockerfile = fmt.Appendf(nil, `
FROM %s
ADD t.tar.gz /
`, baseImage)

	dir = integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateFile("t.tar.gz", buf2.Bytes(), 0600),
	)

	args, trace = f.DFCmdArgs(dir.Name, dir.Name)
	defer os.RemoveAll(trace)

	destDir = t.TempDir()

	cmd = sb.Cmd(args + fmt.Sprintf(" --output type=local,dest=%s", destDir))
	require.NoError(t, cmd.Run())

	dt, err = os.ReadFile(filepath.Join(destDir, "foo"))
	require.NoError(t, err)
	require.Equal(t, expectedContent, dt)

	// add with unpack=false
	dockerfile = fmt.Appendf(nil, `
	FROM %s
	ADD --unpack=false t.tar.gz /
	`, baseImage)

	dir = integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateFile("t.tar.gz", buf2.Bytes(), 0600),
	)

	args, trace = f.DFCmdArgs(dir.Name, dir.Name)
	defer os.RemoveAll(trace)

	destDir = t.TempDir()

	cmd = sb.Cmd(args + fmt.Sprintf(" --output type=local,dest=%s", destDir))
	require.NoError(t, cmd.Run())

	_, err = os.Stat(filepath.Join(destDir, "foo"))
	require.Error(t, err)
	require.True(t, os.IsNotExist(err))

	dt, err = os.ReadFile(filepath.Join(destDir, "t.tar.gz"))
	require.NoError(t, err)
	require.Equal(t, buf2.Bytes(), dt)

	// COPY doesn't extract
	dockerfile = fmt.Appendf(nil, `
FROM %s
COPY t.tar.gz /
`, baseImage)

	dir = integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateFile("t.tar.gz", buf2.Bytes(), 0600),
	)

	args, trace = f.DFCmdArgs(dir.Name, dir.Name)
	defer os.RemoveAll(trace)

	destDir = t.TempDir()

	cmd = sb.Cmd(args + fmt.Sprintf(" --output type=local,dest=%s", destDir))
	require.NoError(t, cmd.Run())

	dt, err = os.ReadFile(filepath.Join(destDir, "t.tar.gz"))
	require.NoError(t, err)
	require.Equal(t, buf2.Bytes(), dt)

	// ADD from URL doesn't extract
	resp := httpserver.Response{
		Etag:    identity.NewID(),
		Content: buf2.Bytes(),
	}

	server := httpserver.NewTestServer(map[string]httpserver.Response{
		"/t.tar.gz": resp,
	})
	defer server.Close()

	dockerfile = fmt.Appendf(nil, `
FROM %s
ADD %s /
`, baseImage, server.URL+"/t.tar.gz")

	dir = integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	args, trace = f.DFCmdArgs(dir.Name, dir.Name)
	defer os.RemoveAll(trace)

	destDir = t.TempDir()

	cmd = sb.Cmd(args + fmt.Sprintf(" --output type=local,dest=%s", destDir))
	require.NoError(t, cmd.Run())

	dt, err = os.ReadFile(filepath.Join(destDir, "t.tar.gz"))
	require.NoError(t, err)
	require.Equal(t, buf2.Bytes(), dt)

	// ADD from URL with --unpack=true
	dockerfile = fmt.Appendf(nil, `
FROM %s
ADD --unpack=true %s /dest/
`, baseImage, server.URL+"/t.tar.gz")

	dir = integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	args, trace = f.DFCmdArgs(dir.Name, dir.Name)
	defer os.RemoveAll(trace)

	destDir = t.TempDir()

	cmd = sb.Cmd(args + fmt.Sprintf(" --output type=local,dest=%s", destDir))
	require.NoError(t, cmd.Run())

	dt, err = os.ReadFile(filepath.Join(destDir, "dest/foo"))
	require.NoError(t, err)
	require.Equal(t, expectedContent, dt)

	// https://github.com/moby/buildkit/issues/386
	dockerfile = fmt.Appendf(nil, `
FROM %s
ADD %s /newname.tar.gz
`, baseImage, server.URL+"/t.tar.gz")

	dir = integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	args, trace = f.DFCmdArgs(dir.Name, dir.Name)
	defer os.RemoveAll(trace)

	destDir = t.TempDir()

	cmd = sb.Cmd(args + fmt.Sprintf(" --output type=local,dest=%s", destDir))
	require.NoError(t, cmd.Run())

	dt, err = os.ReadFile(filepath.Join(destDir, "newname.tar.gz"))
	require.NoError(t, err)
	require.Equal(t, buf2.Bytes(), dt)
}

func testAddChownArchive(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	f := getFrontend(t, sb)

	buf := bytes.NewBuffer(nil)
	tw := tar.NewWriter(buf)
	content := []byte("content0")
	err := tw.WriteHeader(&tar.Header{
		Name:     "foo",
		Typeflag: tar.TypeReg,
		Size:     int64(len(content)),
		Mode:     0644,
	})
	require.NoError(t, err)
	_, err = tw.Write(content)
	require.NoError(t, err)
	err = tw.Close()
	require.NoError(t, err)

	dockerfile := []byte(`
FROM scratch
ADD --chown=100:200 t.tar /out/
`)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateFile("t.tar", buf.Bytes(), 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	outBuf := &bytes.Buffer{}
	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		Exports: []client.ExportEntry{
			{
				Type:   client.ExporterTar,
				Output: fixedWriteCloser(&nopWriteCloser{outBuf}),
			},
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)

	m, err := testutil.ReadTarToMap(outBuf.Bytes(), false)
	require.NoError(t, err)

	mi, ok := m["out/foo"]
	require.True(t, ok)
	require.Equal(t, "content0", string(mi.Data))
	require.Equal(t, 100, mi.Header.Uid)
	require.Equal(t, 200, mi.Header.Gid)

	mi, ok = m["out/"]
	require.True(t, ok)
	require.Equal(t, 100, mi.Header.Uid)
	require.Equal(t, 200, mi.Header.Gid)
}

func testAddArchiveWildcard(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	buf := bytes.NewBuffer(nil)
	tw := tar.NewWriter(buf)
	expectedContent := []byte("content0")
	err := tw.WriteHeader(&tar.Header{
		Name:     "foo",
		Typeflag: tar.TypeReg,
		Size:     int64(len(expectedContent)),
		Mode:     0644,
	})
	require.NoError(t, err)
	_, err = tw.Write(expectedContent)
	require.NoError(t, err)
	err = tw.Close()
	require.NoError(t, err)

	buf2 := bytes.NewBuffer(nil)
	tw = tar.NewWriter(buf2)
	expectedContent = []byte("content1")
	err = tw.WriteHeader(&tar.Header{
		Name:     "bar",
		Typeflag: tar.TypeReg,
		Size:     int64(len(expectedContent)),
		Mode:     0644,
	})
	require.NoError(t, err)
	_, err = tw.Write(expectedContent)
	require.NoError(t, err)
	err = tw.Close()
	require.NoError(t, err)

	dockerfile := []byte(integration.UnixOrWindows(
		`
FROM scratch
ADD *.tar /dest
`,
		`
FROM nanoserver
ADD *.tar /dest
`,
	))

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateFile("t.tar", buf.Bytes(), 0600),
		fstest.CreateFile("b.tar", buf2.Bytes(), 0600),
	)

	destDir := t.TempDir()

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		Exports: []client.ExportEntry{
			{
				Type:      client.ExporterLocal,
				OutputDir: destDir,
			},
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "dest/foo"))
	require.NoError(t, err)
	require.Equal(t, "content0", string(dt))

	dt, err = os.ReadFile(filepath.Join(destDir, "dest/bar"))
	require.NoError(t, err)
	require.Equal(t, "content1", string(dt))
}

func testAddChownExpand(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	f := getFrontend(t, sb)

	dockerfile := []byte(`
FROM busybox
ARG group
ENV owner 1000
ADD --chown=${owner}:${group} foo /
RUN [ "$(stat -c "%u %G" /foo)" == "1000 nobody" ]
`)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateFile("foo", []byte(`foo-contents`), 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"build-arg:group": "nobody",
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)
}

func testAddURLChmod(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	f := getFrontend(t, sb)
	f.RequiresBuildctl(t)

	resp := httpserver.Response{
		Etag:    identity.NewID(),
		Content: []byte("content1"),
	}
	server := httpserver.NewTestServer(map[string]httpserver.Response{
		"/foo": resp,
	})
	defer server.Close()

	dockerfile := fmt.Appendf(nil, `
FROM busybox AS build
ADD --chmod=644 %[1]s /tmp/foo1
ADD --chmod=755 %[1]s /tmp/foo2
ADD --chmod=0413 %[1]s /tmp/foo3

ARG mode
ADD --chmod=${mode} %[1]s /tmp/foo4

RUN stat -c "%%04a" /tmp/foo1 >> /dest && \
	stat -c "%%04a" /tmp/foo2 >> /dest && \
	stat -c "%%04a" /tmp/foo3 >> /dest && \
	stat -c "%%04a" /tmp/foo4 >> /dest

FROM scratch
COPY --from=build /dest /dest
`, server.URL+"/foo")

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	destDir := t.TempDir()

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		Exports: []client.ExportEntry{
			{
				Type:      client.ExporterLocal,
				OutputDir: destDir,
			},
		},
		FrontendAttrs: map[string]string{
			"build-arg:mode": "400",
		},
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)

	dt, err := os.ReadFile(filepath.Join(destDir, "dest"))
	require.NoError(t, err)
	require.Equal(t, []byte("0644\n0755\n0413\n0400\n"), dt)
}

func testAddInvalidChmod(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	f := getFrontend(t, sb)

	dockerfile := []byte(`
FROM scratch
ADD --chmod=64a foo /
`)

	dir := integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateFile("foo", []byte(`foo-contents`), 0600),
	)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.ErrorContains(t, err, "invalid chmod parameter: '64a'. it should be octal string and between 0 and 07777")

	dockerfile = []byte(`
FROM scratch
ADD --chmod=10000 foo /
`)

	dir = integration.Tmpdir(
		t,
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateFile("foo", []byte(`foo-contents`), 0600),
	)

	c, err = client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		LocalMounts: map[string]fsutil.FS{
			dockerui.DefaultLocalNameDockerfile: dir,
			dockerui.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.ErrorContains(t, err, "invalid chmod parameter: '10000'. it should be octal string and between 0 and 07777")
}
