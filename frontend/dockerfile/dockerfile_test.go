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
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/containerd/containerd"
	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/fs/fstest"
	"github.com/containerd/containerd/namespaces"
	"github.com/containerd/containerd/platforms"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/frontend/dockerfile/builder"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/util/testutil/httpserver"
	"github.com/moby/buildkit/util/testutil/integration"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestIntegration(t *testing.T) {
	integration.Run(t, []integration.Test{
		testDockerfileDirs,
		testDockerfileInvalidCommand,
		testDockerfileADDFromURL,
		testDockerfileAddArchive,
		testDockerfileScratchConfig,
		testExportedHistory,
		testExposeExpansion,
		testUser,
		testDockerignore,
		testDockerfileFromGit,
		testCopyChown,
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
	require.Contains(t, stdout.String(), "executor failed running")
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
	require.Equal(t, 0, len(ociimg.RootFS.DiffIDs))

	require.Equal(t, 1, len(ociimg.History))
	require.Contains(t, ociimg.History[0].CreatedBy, "ENV foo=bar")
	require.Equal(t, true, ociimg.History[0].EmptyLayer)

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

func testExposeExpansion(t *testing.T, sb integration.Sandbox) {
	t.Parallel()

	dockerfile := []byte(`
FROM scratch
ARG PORTS="3000 4000/udp"
EXPOSE $PORTS
EXPOSE 5000
`)

	dir, err := tmpdir(
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)
	require.NoError(t, err)
	defer os.RemoveAll(dir)

	c, err := client.New(sb.Address())
	require.NoError(t, err)
	defer c.Close()

	target := "example.com/moby/dockerfileexpansion:test"
	err = c.Solve(context.TODO(), nil, client.SolveOpt{
		Frontend: "dockerfile.v0",
		Exporter: client.ExporterImage,
		ExporterAttrs: map[string]string{
			"name": target,
		},
		LocalDirs: map[string]string{
			builder.LocalNameDockerfile: dir,
			builder.LocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)

	var cdAddress string
	if cd, ok := sb.(interface {
		ContainerdAddress() string
	}); !ok {
		return
	} else {
		cdAddress = cd.ContainerdAddress()
	}

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

	require.Equal(t, 3, len(ociimg.Config.ExposedPorts))

	var ports []string
	for p := range ociimg.Config.ExposedPorts {
		ports = append(ports, p)
	}

	sort.Strings(ports)

	require.Equal(t, "3000/tcp", ports[0])
	require.Equal(t, "4000/udp", ports[1])
	require.Equal(t, "5000/tcp", ports[2])
}

func testDockerignore(t *testing.T, sb integration.Sandbox) {
	t.Parallel()

	dockerfile := []byte(`
FROM scratch
COPY . .
`)

	dockerignore := []byte(`
ba*
Dockerfile
!bay
.dockerignore
`)

	dir, err := tmpdir(
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateFile("foo", []byte(`foo-contents`), 0600),
		fstest.CreateFile("bar", []byte(`bar-contents`), 0600),
		fstest.CreateFile("baz", []byte(`baz-contents`), 0600),
		fstest.CreateFile("bay", []byte(`bay-contents`), 0600),
		fstest.CreateFile(".dockerignore", dockerignore, 0600),
	)
	require.NoError(t, err)
	defer os.RemoveAll(dir)

	c, err := client.New(sb.Address())
	require.NoError(t, err)
	defer c.Close()

	destDir, err := ioutil.TempDir("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(destDir)

	err = c.Solve(context.TODO(), nil, client.SolveOpt{
		Frontend: "dockerfile.v0",
		Exporter: client.ExporterLocal,
		ExporterAttrs: map[string]string{
			"output": destDir,
		},
		LocalDirs: map[string]string{
			builder.LocalNameDockerfile: dir,
			builder.LocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)

	dt, err := ioutil.ReadFile(filepath.Join(destDir, "foo"))
	require.NoError(t, err)
	require.Equal(t, "foo-contents", string(dt))

	_, err = os.Stat(filepath.Join(destDir, ".dockerignore"))
	require.Error(t, err)
	require.True(t, os.IsNotExist(err))

	_, err = os.Stat(filepath.Join(destDir, "Dockerfile"))
	require.Error(t, err)
	require.True(t, os.IsNotExist(err))

	_, err = os.Stat(filepath.Join(destDir, "bar"))
	require.Error(t, err)
	require.True(t, os.IsNotExist(err))

	_, err = os.Stat(filepath.Join(destDir, "baz"))
	require.Error(t, err)
	require.True(t, os.IsNotExist(err))

	dt, err = ioutil.ReadFile(filepath.Join(destDir, "bay"))
	require.NoError(t, err)
	require.Equal(t, "bay-contents", string(dt))
}

func testExportedHistory(t *testing.T, sb integration.Sandbox) {
	t.Parallel()

	// using multi-stage to test that history is scoped to one stage
	dockerfile := []byte(`
FROM busybox AS base
ENV foo=bar
COPY foo /foo2
FROM busybox
COPY --from=base foo2 foo3
WORKDIR /
RUN echo bar > foo4
RUN ["ls"]
`)

	dir, err := tmpdir(
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateFile("foo", []byte("contents0"), 0600),
	)
	require.NoError(t, err)
	defer os.RemoveAll(dir)

	args, trace := dfCmdArgs(dir, dir)
	defer os.RemoveAll(trace)

	target := "example.com/moby/dockerfilescratch:test"
	cmd := sb.Cmd(args + " --exporter=image --exporter-opt=name=" + target)
	require.NoError(t, cmd.Run())

	// TODO: expose this test to OCI worker

	var cdAddress string
	if cd, ok := sb.(interface {
		ContainerdAddress() string
	}); !ok {
		t.Skip("only for containerd worker")
	} else {
		cdAddress = cd.ContainerdAddress()
	}

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

	require.Equal(t, "layers", ociimg.RootFS.Type)
	// this depends on busybox. should be ok after freezing images
	require.Equal(t, 3, len(ociimg.RootFS.DiffIDs))

	require.Equal(t, 6, len(ociimg.History))
	require.Contains(t, ociimg.History[2].CreatedBy, "COPY foo2 foo3")
	require.Equal(t, false, ociimg.History[2].EmptyLayer)
	require.Contains(t, ociimg.History[3].CreatedBy, "WORKDIR /")
	require.Equal(t, true, ociimg.History[3].EmptyLayer)
	require.Contains(t, ociimg.History[4].CreatedBy, "echo bar > foo4")
	require.Equal(t, false, ociimg.History[4].EmptyLayer)
	require.Contains(t, ociimg.History[5].CreatedBy, "RUN ls")
	require.Equal(t, true, ociimg.History[5].EmptyLayer)
}

func testUser(t *testing.T, sb integration.Sandbox) {
	t.Parallel()

	dockerfile := []byte(`
FROM busybox AS base
RUN mkdir -m 0777 /out
RUN id -un > /out/rootuser
USER daemon
RUN id -un > /out/daemonuser
FROM scratch
COPY --from=base /out /
USER nobody
`)

	dir, err := tmpdir(
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)
	require.NoError(t, err)
	defer os.RemoveAll(dir)

	c, err := client.New(sb.Address())
	require.NoError(t, err)
	defer c.Close()

	destDir, err := ioutil.TempDir("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(destDir)

	err = c.Solve(context.TODO(), nil, client.SolveOpt{
		Frontend: "dockerfile.v0",
		Exporter: client.ExporterLocal,
		ExporterAttrs: map[string]string{
			"output": destDir,
		},
		LocalDirs: map[string]string{
			builder.LocalNameDockerfile: dir,
			builder.LocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)

	dt, err := ioutil.ReadFile(filepath.Join(destDir, "rootuser"))
	require.NoError(t, err)
	require.Equal(t, string(dt), "root\n")

	dt, err = ioutil.ReadFile(filepath.Join(destDir, "daemonuser"))
	require.NoError(t, err)
	require.Equal(t, string(dt), "daemon\n")

	// test user in exported
	target := "example.com/moby/dockerfileuser:test"
	err = c.Solve(context.TODO(), nil, client.SolveOpt{
		Frontend: "dockerfile.v0",
		Exporter: client.ExporterImage,
		ExporterAttrs: map[string]string{
			"name": target,
		},
		LocalDirs: map[string]string{
			builder.LocalNameDockerfile: dir,
			builder.LocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)

	var cdAddress string
	if cd, ok := sb.(interface {
		ContainerdAddress() string
	}); !ok {
		return
	} else {
		cdAddress = cd.ContainerdAddress()
	}

	client, err := containerd.New(cdAddress)
	require.NoError(t, err)
	defer client.Close()

	ctx := namespaces.WithNamespace(context.Background(), "buildkit")

	img, err := client.ImageService().Get(ctx, target)
	require.NoError(t, err)

	desc, err := img.Config(ctx, client.ContentStore(), platforms.Default())
	require.NoError(t, err)

	dt, err = content.ReadBlob(ctx, client.ContentStore(), desc.Digest)
	require.NoError(t, err)

	var ociimg ocispec.Image
	err = json.Unmarshal(dt, &ociimg)
	require.NoError(t, err)

	require.Equal(t, "nobody", ociimg.Config.User)
}

func testCopyChown(t *testing.T, sb integration.Sandbox) {
	t.Parallel()

	dockerfile := []byte(`
FROM busybox AS base
RUN mkdir -m 0777 /out
COPY --chown=daemon foo /
COPY --chown=1000:nogroup bar /baz
RUN stat -c "%U %G" /foo  > /out/fooowner
RUN stat -c "%u %G" /baz/sub  > /out/subowner
FROM scratch
COPY --from=base /out /
`)

	dir, err := tmpdir(
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateFile("foo", []byte(`foo-contents`), 0600),
		fstest.CreateDir("bar", 0700),
		fstest.CreateFile("bar/sub", nil, 0600),
	)
	require.NoError(t, err)
	defer os.RemoveAll(dir)

	c, err := client.New(sb.Address())
	require.NoError(t, err)
	defer c.Close()

	destDir, err := ioutil.TempDir("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(destDir)

	err = c.Solve(context.TODO(), nil, client.SolveOpt{
		Frontend: "dockerfile.v0",
		Exporter: client.ExporterLocal,
		ExporterAttrs: map[string]string{
			"output": destDir,
		},
		LocalDirs: map[string]string{
			builder.LocalNameDockerfile: dir,
			builder.LocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)

	dt, err := ioutil.ReadFile(filepath.Join(destDir, "fooowner"))
	require.NoError(t, err)
	require.Equal(t, string(dt), "daemon daemon\n")

	dt, err = ioutil.ReadFile(filepath.Join(destDir, "subowner"))
	require.NoError(t, err)
	require.Equal(t, string(dt), "1000 nogroup\n")
}

func testDockerfileFromGit(t *testing.T, sb integration.Sandbox) {
	t.Parallel()

	gitDir, err := ioutil.TempDir("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(gitDir)

	dockerfile := `
FROM busybox AS build
RUN echo -n fromgit > foo	
FROM scratch
COPY --from=build foo bar
`

	err = ioutil.WriteFile(filepath.Join(gitDir, "Dockerfile"), []byte(dockerfile), 0600)
	require.NoError(t, err)

	err = runShell(gitDir,
		"git init",
		"git config --local user.email test",
		"git config --local user.name test",
		"git add Dockerfile",
		"git commit -m initial",
		"git branch first",
	)
	require.NoError(t, err)

	dockerfile += `
COPY --from=build foo bar2
`

	err = ioutil.WriteFile(filepath.Join(gitDir, "Dockerfile"), []byte(dockerfile), 0600)
	require.NoError(t, err)

	err = runShell(gitDir,
		"git add Dockerfile",
		"git commit -m second",
		"git update-server-info",
	)
	require.NoError(t, err)

	server := httptest.NewServer(http.FileServer(http.Dir(filepath.Join(gitDir, ".git"))))
	defer server.Close()

	destDir, err := ioutil.TempDir("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(destDir)

	c, err := client.New(sb.Address())
	require.NoError(t, err)
	defer c.Close()

	err = c.Solve(context.TODO(), nil, client.SolveOpt{
		Frontend: "dockerfile.v0",
		FrontendAttrs: map[string]string{
			"context": "git://" + server.URL + "/#first",
		},
		Exporter: client.ExporterLocal,
		ExporterAttrs: map[string]string{
			"output": destDir,
		},
	}, nil)
	require.NoError(t, err)

	dt, err := ioutil.ReadFile(filepath.Join(destDir, "bar"))
	require.NoError(t, err)
	require.Equal(t, "fromgit", string(dt))

	_, err = os.Stat(filepath.Join(destDir, "bar2"))
	require.Error(t, err)
	require.True(t, os.IsNotExist(err))

	// second request from master branch contains both files
	destDir, err = ioutil.TempDir("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(destDir)

	err = c.Solve(context.TODO(), nil, client.SolveOpt{
		Frontend: "dockerfile.v0",
		FrontendAttrs: map[string]string{
			"context": "git://" + server.URL + "/",
		},
		Exporter: client.ExporterLocal,
		ExporterAttrs: map[string]string{
			"output": destDir,
		},
	}, nil)
	require.NoError(t, err)

	dt, err = ioutil.ReadFile(filepath.Join(destDir, "bar"))
	require.NoError(t, err)
	require.Equal(t, "fromgit", string(dt))

	dt, err = ioutil.ReadFile(filepath.Join(destDir, "bar2"))
	require.NoError(t, err)
	require.Equal(t, "fromgit", string(dt))
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

func runShell(dir string, cmds ...string) error {
	for _, args := range cmds {
		cmd := exec.Command("sh", "-c", args)
		cmd.Dir = dir
		if err := cmd.Run(); err != nil {
			return errors.Wrapf(err, "error running %v", args)
		}
	}
	return nil
}
