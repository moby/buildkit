package dockerfile

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/containerd/continuity/fs/fstest"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/frontend/dockerfile/builder"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/stretchr/testify/require"
)

var mountTests = []integration.Test{
	testMountContext,
	testMountTmpfs,
	testMountRWCache,
	testCacheMountDefaultID,
	testMountEnvVar,
	testMountArg,
	testMountEnvAcrossStages,
	testMountMetaArg,
	testMountFromError,
}

func init() {
	allTests = append(allTests, mountTests...)

	fileOpTests = append(fileOpTests, testCacheMountUser)
}

func testMountContext(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfile := []byte(`
FROM busybox
RUN --mount=target=/context [ "$(cat /context/testfile)" == "contents0" ]
`)

	dir, err := tmpdir(
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateFile("testfile", []byte("contents0"), 0600),
	)
	require.NoError(t, err)
	defer os.RemoveAll(dir)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		LocalDirs: map[string]string{
			builder.DefaultLocalNameDockerfile: dir,
			builder.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)
}

func testMountTmpfs(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfile := []byte(`
FROM busybox
RUN --mount=target=/mytmp,type=tmpfs touch /mytmp/foo
RUN [ ! -f /mytmp/foo ]
`)

	dir, err := tmpdir(
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)
	require.NoError(t, err)
	defer os.RemoveAll(dir)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		LocalDirs: map[string]string{
			builder.DefaultLocalNameDockerfile: dir,
			builder.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)
}

func testMountRWCache(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfile := []byte(`
from busybox AS build
copy cachebust /
run mkdir out && echo foo > out/foo

from busybox as second
RUN --mount=from=build,src=out,target=/out,rw touch /out/bar && cat /dev/urandom | head -c 100 | sha256sum > /unique

from scratch
COPY --from=second /unique /unique
`)

	dir, err := tmpdir(
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateFile("cachebust", []byte("0"), 0600),
	)
	require.NoError(t, err)
	defer os.RemoveAll(dir)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	destDir, err := ioutil.TempDir("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(destDir)

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		Exports: []client.ExportEntry{
			{
				Type:      client.ExporterLocal,
				OutputDir: destDir,
			},
		},
		LocalDirs: map[string]string{
			builder.DefaultLocalNameDockerfile: dir,
			builder.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)

	dt1, err := ioutil.ReadFile(filepath.Join(destDir, "unique"))
	require.NoError(t, err)

	// repeat with changed file that should be still cached by content
	dir, err = tmpdir(
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
		fstest.CreateFile("cachebust", []byte("1"), 0600),
	)
	require.NoError(t, err)
	defer os.RemoveAll(dir)

	destDir, err = ioutil.TempDir("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(destDir)

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		Exports: []client.ExportEntry{
			{
				Type:      client.ExporterLocal,
				OutputDir: destDir,
			},
		},
		LocalDirs: map[string]string{
			builder.DefaultLocalNameDockerfile: dir,
			builder.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)

	dt2, err := ioutil.ReadFile(filepath.Join(destDir, "unique"))
	require.NoError(t, err)
	require.Equal(t, dt1, dt2)
}

func testCacheMountUser(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)
	isFileOp := getFileOp(t, sb)

	dockerfile := []byte(`
FROM busybox
RUN --mount=type=cache,target=/mycache,uid=1001,gid=1002,mode=0751 [ "$(stat -c "%u %g %f" /mycache)" == "1001 1002 41e9" ]
`)

	dir, err := tmpdir(
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)
	require.NoError(t, err)
	defer os.RemoveAll(dir)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		FrontendAttrs: map[string]string{
			"build-arg:BUILDKIT_DISABLE_FILEOP": strconv.FormatBool(!isFileOp),
		},
		LocalDirs: map[string]string{
			builder.DefaultLocalNameDockerfile: dir,
			builder.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)
}

func testCacheMountDefaultID(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfile := []byte(`
FROM busybox
RUN --mount=type=cache,target=/mycache touch /mycache/foo
RUN --mount=type=cache,target=/mycache2 [ ! -f /mycache2/foo ]
RUN --mount=type=cache,target=/mycache [ -f /mycache/foo ]
`)

	dir, err := tmpdir(
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)
	require.NoError(t, err)
	defer os.RemoveAll(dir)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		LocalDirs: map[string]string{
			builder.DefaultLocalNameDockerfile: dir,
			builder.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)
}

func testMountEnvVar(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfile := []byte(`
FROM busybox
ENV SOME_PATH=/mycache
RUN --mount=type=cache,target=/mycache touch /mycache/foo
RUN --mount=type=cache,target=$SOME_PATH [ -f $SOME_PATH/foo ]
`)

	dir, err := tmpdir(
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)
	require.NoError(t, err)
	defer os.RemoveAll(dir)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		LocalDirs: map[string]string{
			builder.DefaultLocalNameDockerfile: dir,
			builder.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)
}

func testMountArg(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfile := []byte(`
FROM busybox
ARG MNT_TYPE=cache
RUN --mount=type=$MNT_TYPE,target=/mycache2 touch /mycache2/foo
RUN --mount=type=cache,target=/mycache2 [ -f /mycache2/foo ]
`)

	dir, err := tmpdir(
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)
	require.NoError(t, err)
	defer os.RemoveAll(dir)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		LocalDirs: map[string]string{
			builder.DefaultLocalNameDockerfile: dir,
			builder.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)
}

func testMountEnvAcrossStages(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfile := []byte(`
FROM busybox as stage1

ENV MNT_ID=mycache
ENV MNT_TYPE2=cache
RUN --mount=type=cache,id=mycache,target=/abcabc touch /abcabc/foo
RUN --mount=type=$MNT_TYPE2,id=$MNT_ID,target=/cbacba [ -f /cbacba/foo ]

FROM stage1
RUN --mount=type=$MNT_TYPE2,id=$MNT_ID,target=/whatever [ -f /whatever/foo ]
`)

	dir, err := tmpdir(
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)
	require.NoError(t, err)
	defer os.RemoveAll(dir)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		LocalDirs: map[string]string{
			builder.DefaultLocalNameDockerfile: dir,
			builder.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)
}

func testMountMetaArg(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfile := []byte(`
ARG META_PATH=/tmp/meta

FROM busybox
ARG META_PATH
RUN --mount=type=cache,id=mycache,target=/tmp/meta touch /tmp/meta/foo
RUN --mount=type=cache,id=mycache,target=$META_PATH [ -f /tmp/meta/foo ]
`)

	dir, err := tmpdir(
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)
	require.NoError(t, err)
	defer os.RemoveAll(dir)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		LocalDirs: map[string]string{
			builder.DefaultLocalNameDockerfile: dir,
			builder.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.NoError(t, err)
}

func testMountFromError(t *testing.T, sb integration.Sandbox) {
	f := getFrontend(t, sb)

	dockerfile := []byte(`
FROM busybox as test
RUN touch /tmp/test

FROM busybox
ENV ttt=test
RUN --mount=from=$ttt,type=cache,target=/tmp ls
`)

	dir, err := tmpdir(
		fstest.CreateFile("Dockerfile", dockerfile, 0600),
	)
	require.NoError(t, err)
	defer os.RemoveAll(dir)

	c, err := client.New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	_, err = f.Solve(sb.Context(), c, client.SolveOpt{
		LocalDirs: map[string]string{
			builder.DefaultLocalNameDockerfile: dir,
			builder.DefaultLocalNameContext:    dir,
		},
	}, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "'from' doesn't support variable expansion, define alias stage instead")
}
