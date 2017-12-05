package dockerfile

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/containerd/containerd"
	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/fs/fstest"
	"github.com/containerd/containerd/namespaces"
	"github.com/containerd/containerd/platforms"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/util/testutil/httpserver"
	"github.com/moby/buildkit/util/testutil/integration"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

func TestIntegration(t *testing.T) {
	integration.Run(t, []integration.Test{
		testDockerfileDirs,
		testDockerfileInvalidCommand,
		testDockerfileADDFromURL,
		testDockerfileAddArchive,
		testDockerfileScratchConfig,
	})
}

func testDockerfileDirs(t *testing.T, sb integration.Sandbox) {
	t.Parallel()
	dockerfile := []byte(`
	FROM busybox
	COPY foo /foo2
	COPY foo /
	RUN echo -n bar > foo3
	RUN test -f foo
	RUN cmp -s foo foo2
	RUN cmp -s foo foo3
`)

	dir, err := tmpdir(
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateFile("foo", []byte("bar"), 0600),
	)
	require.NoError(t, err)
	defer os.RemoveAll(dir)

	args, trace := dfCmdArgs(dir, dir)
	defer os.RemoveAll(trace)

	cmd := sb.Cmd(args)
	require.NoError(t, cmd.Run())

	_, err = os.Stat(trace)
	require.NoError(t, err)

	// relative urls
	args, trace = dfCmdArgs(".", ".")
	defer os.RemoveAll(trace)

	cmd = sb.Cmd(args)
	cmd.Dir = dir
	require.NoError(t, cmd.Run())

	_, err = os.Stat(trace)
	require.NoError(t, err)

	// different context and dockerfile directories
	dir1, err := tmpdir(
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)
	require.NoError(t, err)
	defer os.RemoveAll(dir1)

	dir2, err := tmpdir(
		fstest.CreateFile("foo", []byte("bar"), 0600),
	)
	require.NoError(t, err)
	defer os.RemoveAll(dir2)

	args, trace = dfCmdArgs(dir2, dir1)
	defer os.RemoveAll(trace)

	cmd = sb.Cmd(args)
	cmd.Dir = dir
	require.NoError(t, cmd.Run())

	_, err = os.Stat(trace)
	require.NoError(t, err)

	// TODO: test trace file output, cache hits, logs etc.
	// TODO: output metadata about original dockerfile command in trace
}

func testDockerfileInvalidCommand(t *testing.T, sb integration.Sandbox) {
	t.Parallel()
	dockerfile := []byte(`
	FROM busybox
	RUN invalidcmd
`)

	dir, err := tmpdir(
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)
	require.NoError(t, err)
	defer os.RemoveAll(dir)

	args, trace := dfCmdArgs(dir, dir)
	defer os.RemoveAll(trace)

	cmd := sb.Cmd(args)
	stdout := new(bytes.Buffer)
	cmd.Stderr = stdout
	err = cmd.Run()
	require.Error(t, err)
	require.Contains(t, stdout.String(), "/bin/sh -c invalidcmd")
	require.Contains(t, stdout.String(), "worker failed running")
}

func testDockerfileADDFromURL(t *testing.T, sb integration.Sandbox) {
	t.Parallel()

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

	dockerfile := []byte(fmt.Sprintf(`
FROM scratch
ADD %s /dest/
`, server.URL+"/foo"))

	dir, err := tmpdir(
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)
	require.NoError(t, err)
	defer os.RemoveAll(dir)

	args, trace := dfCmdArgs(dir, dir)
	defer os.RemoveAll(trace)

	destDir, err := tmpdir()
	require.NoError(t, err)
	defer os.RemoveAll(destDir)

	cmd := sb.Cmd(args + fmt.Sprintf(" --exporter=local --exporter-opt output=%s", destDir))
	err = cmd.Run()
	require.NoError(t, err)

	dt, err := ioutil.ReadFile(filepath.Join(destDir, "dest/foo"))
	require.NoError(t, err)
	require.Equal(t, []byte("content1"), dt)

	// test the default properties
	dockerfile = []byte(fmt.Sprintf(`
FROM scratch
ADD %s /dest/
`, server.URL+"/"))

	dir, err = tmpdir(
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)
	require.NoError(t, err)
	defer os.RemoveAll(dir)

	args, trace = dfCmdArgs(dir, dir)
	defer os.RemoveAll(trace)

	destDir, err = tmpdir()
	require.NoError(t, err)
	defer os.RemoveAll(destDir)

	cmd = sb.Cmd(args + fmt.Sprintf(" --exporter=local --exporter-opt output=%s", destDir))
	err = cmd.Run()
	require.NoError(t, err)

	destFile := filepath.Join(destDir, "dest/__unnamed__")
	dt, err = ioutil.ReadFile(destFile)
	require.NoError(t, err)
	require.Equal(t, []byte("content2"), dt)

	fi, err := os.Stat(destFile)
	require.NoError(t, err)
	require.Equal(t, fi.ModTime().Format(http.TimeFormat), modTime.Format(http.TimeFormat))
}

func testDockerfileAddArchive(t *testing.T, sb integration.Sandbox) {
	t.Parallel()

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

	dockerfile := []byte(`
FROM scratch
ADD t.tar /
`)

	dir, err := tmpdir(
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateFile("t.tar", buf.Bytes(), 0600),
	)
	require.NoError(t, err)
	defer os.RemoveAll(dir)

	args, trace := dfCmdArgs(dir, dir)
	defer os.RemoveAll(trace)

	destDir, err := tmpdir()
	require.NoError(t, err)
	defer os.RemoveAll(destDir)

	cmd := sb.Cmd(args + fmt.Sprintf(" --exporter=local --exporter-opt output=%s", destDir))
	require.NoError(t, cmd.Run())

	dt, err := ioutil.ReadFile(filepath.Join(destDir, "foo"))
	require.NoError(t, err)
	require.Equal(t, expectedContent, dt)

	// add gzip tar
	buf2 := bytes.NewBuffer(nil)
	gz := gzip.NewWriter(buf2)
	_, err = gz.Write(buf.Bytes())
	require.NoError(t, err)
	err = gz.Close()
	require.NoError(t, err)

	dockerfile = []byte(`
FROM scratch
ADD t.tar.gz /
`)

	dir, err = tmpdir(
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateFile("t.tar.gz", buf2.Bytes(), 0600),
	)
	require.NoError(t, err)
	defer os.RemoveAll(dir)

	args, trace = dfCmdArgs(dir, dir)
	defer os.RemoveAll(trace)

	destDir, err = tmpdir()
	require.NoError(t, err)
	defer os.RemoveAll(destDir)

	cmd = sb.Cmd(args + fmt.Sprintf(" --exporter=local --exporter-opt output=%s", destDir))
	require.NoError(t, cmd.Run())

	dt, err = ioutil.ReadFile(filepath.Join(destDir, "foo"))
	require.NoError(t, err)
	require.Equal(t, expectedContent, dt)

	// COPY doesn't extract
	dockerfile = []byte(`
FROM scratch
COPY t.tar.gz /
`)

	dir, err = tmpdir(
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateFile("t.tar.gz", buf2.Bytes(), 0600),
	)
	require.NoError(t, err)
	defer os.RemoveAll(dir)

	args, trace = dfCmdArgs(dir, dir)
	defer os.RemoveAll(trace)

	destDir, err = tmpdir()
	require.NoError(t, err)
	defer os.RemoveAll(destDir)

	cmd = sb.Cmd(args + fmt.Sprintf(" --exporter=local --exporter-opt output=%s", destDir))
	require.NoError(t, cmd.Run())

	dt, err = ioutil.ReadFile(filepath.Join(destDir, "t.tar.gz"))
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

	dockerfile = []byte(fmt.Sprintf(`
FROM scratch
ADD %s /
`, server.URL+"/t.tar.gz"))

	dir, err = tmpdir(
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)
	require.NoError(t, err)
	defer os.RemoveAll(dir)

	args, trace = dfCmdArgs(dir, dir)
	defer os.RemoveAll(trace)

	destDir, err = tmpdir()
	require.NoError(t, err)
	defer os.RemoveAll(destDir)

	cmd = sb.Cmd(args + fmt.Sprintf(" --exporter=local --exporter-opt output=%s", destDir))
	require.NoError(t, cmd.Run())

	dt, err = ioutil.ReadFile(filepath.Join(destDir, "t.tar.gz"))
	require.NoError(t, err)
	require.Equal(t, buf2.Bytes(), dt)
}

func testDockerfileScratchConfig(t *testing.T, sb integration.Sandbox) {
	var cdAddress string
	if cd, ok := sb.(interface {
		ContainerdAddress() string
	}); !ok {
		t.Skip("only for containerd worker")
	} else {
		cdAddress = cd.ContainerdAddress()
	}

	t.Parallel()
	dockerfile := []byte(`
FROM scratch
ENV foo=bar
`)

	dir, err := tmpdir(
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)
	require.NoError(t, err)
	defer os.RemoveAll(dir)

	args, trace := dfCmdArgs(dir, dir)
	defer os.RemoveAll(trace)

	target := "example.com/moby/dockerfilescratch:test"
	cmd := sb.Cmd(args + " --exporter=image --exporter-opt=name=" + target)
	err = cmd.Run()
	require.NoError(t, err)

	client, err := containerd.New(cdAddress)
	require.NoError(t, err)
	defer client.Close()

	ctx := namespaces.WithNamespace(context.Background(), "buildkit")

	img, err := client.ImageService().Get(ctx, target)
	require.NoError(t, err)

	desc, err := img.Config(ctx, client.ContentStore(), platforms.Default())
	require.NoError(t, err)

	dt, err := content.ReadBlob(ctx, client.ContentStore(), desc.Digest)
	require.NoError(t, err)

	var ociimg ocispec.Image
	err = json.Unmarshal(dt, &ociimg)
	require.NoError(t, err)

	require.NotEqual(t, "", ociimg.OS)
	require.NotEqual(t, "", ociimg.Architecture)
	require.NotEqual(t, "", ociimg.Config.WorkingDir)
	require.Equal(t, "layers", ociimg.RootFS.Type)

	require.Contains(t, ociimg.Config.Env, "foo=bar")
	require.Condition(t, func() bool {
		for _, env := range ociimg.Config.Env {
			if strings.HasPrefix(env, "PATH=") {
				return true
			}
		}
		return false
	})
}

func tmpdir(appliers ...fstest.Applier) (string, error) {
	tmpdir, err := ioutil.TempDir("", "buildkit-dockerfile")
	if err != nil {
		return "", err
	}
	if err := fstest.Apply(appliers...).Apply(tmpdir); err != nil {
		return "", err
	}
	return tmpdir, nil
}

func dfCmdArgs(ctx, dockerfile string) (string, string) {
	traceFile := filepath.Join(os.TempDir(), "trace"+identity.NewID())
	return fmt.Sprintf("build --no-progress --frontend dockerfile.v0 --local context=%s --local dockerfile=%s --trace=%s", ctx, dockerfile, traceFile), traceFile
}
