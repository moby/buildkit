package dockerfile

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"

	"github.com/containerd/containerd/fs/fstest"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/stretchr/testify/require"
)

func TestIntegration(t *testing.T) {
	integration.Run(t, []integration.Test{
		testDockerfileDirs,
		testDockerfileInvalidCommand,
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
