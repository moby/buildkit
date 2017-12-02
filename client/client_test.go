package client

import (
	"context"
	"io/ioutil"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/util/testutil/httpserver"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/stretchr/testify/require"
)

func TestClientIntegration(t *testing.T) {
	integration.Run(t, []integration.Test{
		testCallDiskUsage,
		testBuildMultiMount,
		testBuildHTTPSource,
	})
}

func testCallDiskUsage(t *testing.T, sb integration.Sandbox) {
	c, err := New(sb.Address())
	require.NoError(t, err)
	defer c.Close()
	_, err = c.DiskUsage(context.TODO())
	require.NoError(t, err)
}

func testBuildMultiMount(t *testing.T, sb integration.Sandbox) {
	requiresLinux(t)
	t.Parallel()
	c, err := New(sb.Address())
	require.NoError(t, err)
	defer c.Close()

	alpine := llb.Image("docker.io/library/alpine:latest")
	ls := alpine.Run(llb.Shlex("/bin/ls -l"))
	busybox := llb.Image("docker.io/library/busybox:latest")
	cp := ls.Run(llb.Shlex("/bin/cp -a /busybox/etc/passwd baz"))
	cp.AddMount("/busybox", busybox)

	def, err := cp.Marshal()
	require.NoError(t, err)

	err = c.Solve(context.TODO(), def, SolveOpt{}, nil)
	require.NoError(t, err)
}

func testBuildHTTPSource(t *testing.T, sb integration.Sandbox) {
	t.Parallel()

	c, err := New(sb.Address())
	require.NoError(t, err)
	defer c.Close()

	resp := httpserver.Response{
		Etag:    identity.NewID(),
		Content: []byte("content1"),
	}

	server := httpserver.NewTestServer(map[string]httpserver.Response{
		"/foo": resp,
	})
	defer server.Close()

	// invalid URL first
	st := llb.HTTP(server.URL + "/bar")

	def, err := st.Marshal()
	require.NoError(t, err)

	err = c.Solve(context.TODO(), def, SolveOpt{}, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid response status 404")

	// first correct request
	st = llb.HTTP(server.URL + "/foo")

	def, err = st.Marshal()
	require.NoError(t, err)

	err = c.Solve(context.TODO(), def, SolveOpt{}, nil)
	require.NoError(t, err)

	require.Equal(t, server.Stats("/foo").AllRequests, 1)
	require.Equal(t, server.Stats("/foo").CachedRequests, 0)

	tmpdir, err := ioutil.TempDir("", "buildkit")
	require.NoError(t, err)
	defer os.RemoveAll(tmpdir)

	err = c.Solve(context.TODO(), def, SolveOpt{
		Exporter: ExporterLocal,
		ExporterAttrs: map[string]string{
			exporterLocalOutputDir: tmpdir,
		},
	}, nil)
	require.NoError(t, err)

	require.Equal(t, server.Stats("/foo").AllRequests, 2)
	require.Equal(t, server.Stats("/foo").CachedRequests, 1)

	dt, err := ioutil.ReadFile(filepath.Join(tmpdir, "foo"))
	require.NoError(t, err)
	require.Equal(t, []byte("content1"), dt)
	// TODO: check that second request was marked as cached
}

func requiresLinux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skipf("unsupported GOOS: %s", runtime.GOOS)
	}
}
