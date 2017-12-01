package main

import (
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"testing"

	"github.com/containerd/containerd/fs/fstest"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/stretchr/testify/require"
)

func testBuildWithLocalFiles(t *testing.T, sb integration.Sandbox) {
	dir, err := tmpdir(
		fstest.CreateFile("foo", []byte("bar"), 0600),
	)
	require.NoError(t, err)
	defer os.RemoveAll(dir)

	st := llb.Image("busybox").
		Run(llb.Shlex("sh -c 'echo -n bar > foo2'")).
		Run(llb.Shlex("cmp -s /mnt/foo foo2"))

	st.AddMount("/mnt", llb.Local("src"), llb.Readonly)

	rdr, err := marshal(st.Root())
	require.NoError(t, err)

	cmd := sb.Cmd(fmt.Sprintf("build --no-progress --local src=%s", dir))
	cmd.Stdin = rdr

	err = cmd.Run()
	require.NoError(t, err)
}

func marshal(st llb.State) (io.Reader, error) {
	def, err := st.Marshal()
	if err != nil {
		return nil, err
	}
	dt, err := def.ToPB().Marshal()
	if err != nil {
		return nil, err
	}
	return bytes.NewBuffer(dt), nil
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
