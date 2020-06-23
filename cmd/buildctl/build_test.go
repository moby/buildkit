package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/containerd/containerd"
	"github.com/containerd/containerd/namespaces"
	"github.com/containerd/continuity/fs/fstest"
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

	cmd := sb.Cmd(fmt.Sprintf("build --progress=plain --local src=%s", dir))
	cmd.Stdin = rdr

	err = cmd.Run()
	require.NoError(t, err)
}

func testBuildLocalExporter(t *testing.T, sb integration.Sandbox) {
	st := llb.Image("busybox").
		Run(llb.Shlex("sh -c 'echo -n bar > /out/foo'"))

	out := st.AddMount("/out", llb.Scratch())

	rdr, err := marshal(out)
	require.NoError(t, err)

	tmpdir, err := ioutil.TempDir("", "buildkit-buildctl")
	require.NoError(t, err)
	defer os.RemoveAll(tmpdir)

	cmd := sb.Cmd(fmt.Sprintf("build --progress=plain --exporter=local --exporter-opt output=%s", tmpdir))
	cmd.Stdin = rdr
	err = cmd.Run()

	require.NoError(t, err)

	dt, err := ioutil.ReadFile(filepath.Join(tmpdir, "foo"))
	require.NoError(t, err)
	require.Equal(t, string(dt), "bar")
}

func testBuildContainerdExporter(t *testing.T, sb integration.Sandbox) {
	cdAddress := sb.ContainerdAddress()
	if cdAddress == "" {
		t.Skip("test is only for containerd worker")
	}

	st := llb.Image("busybox").
		Run(llb.Shlex("sh -c 'echo -n bar > /foo'"))

	rdr, err := marshal(st.Root())
	require.NoError(t, err)

	imageName := "example.com/moby/imageexporter:test"

	buildCmd := []string{
		"build", "--progress=plain",
		"--exporter=image", "--exporter-opt", "unpack=true",
		"--exporter-opt", "name=" + imageName,
	}

	cmd := sb.Cmd(strings.Join(buildCmd, " "))
	cmd.Stdin = rdr
	err = cmd.Run()
	require.NoError(t, err)

	client, err := containerd.New(cdAddress, containerd.WithTimeout(60*time.Second), containerd.WithDefaultRuntime("io.containerd.runtime.v1.linux"))
	require.NoError(t, err)
	defer client.Close()

	ctx := namespaces.WithNamespace(context.Background(), "buildkit")

	img, err := client.GetImage(ctx, imageName)
	require.NoError(t, err)

	// NOTE: by default, it is overlayfs
	ok, err := img.IsUnpacked(ctx, "overlayfs")
	require.NoError(t, err)
	require.Equal(t, ok, true)
}

func marshal(st llb.State) (io.Reader, error) {
	def, err := st.Marshal(context.TODO())
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
	tmpdir, err := ioutil.TempDir("", "buildkit-buildctl")
	if err != nil {
		return "", err
	}
	if err := fstest.Apply(appliers...).Apply(tmpdir); err != nil {
		return "", err
	}
	return tmpdir, nil
}
