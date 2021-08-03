// +build linux,!no_containerd_worker

package containerd

import (
	"context"
	"io/ioutil"
	"os"
	"testing"

	"github.com/moby/buildkit/util/network/netproviders"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/moby/buildkit/worker/base"
	"github.com/moby/buildkit/worker/tests"
	"github.com/stretchr/testify/require"
)

func init() {
	integration.InitContainerdWorker()
}

func TestContainerdWorkerIntegration(t *testing.T) {
	checkRequirement(t)
	integration.Run(t, []integration.Test{
		testContainerdWorkerExec,
		testContainerdWorkerExecFailures,
	})
}

func newWorkerOpt(t *testing.T, addr string) (base.WorkerOpt, func()) {
	tmpdir, err := ioutil.TempDir("", "workertest")
	require.NoError(t, err)
	cleanup := func() { os.RemoveAll(tmpdir) }
	workerOpt, err := NewWorkerOpt(context.TODO(), tmpdir, addr, "overlayfs", "buildkit-test", nil, nil, netproviders.Opt{Mode: "host"}, "", nil, "")
	require.NoError(t, err)
	return workerOpt, cleanup
}

func checkRequirement(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("requires root")
	}
}

func testContainerdWorkerExec(t *testing.T, sb integration.Sandbox) {
	workerOpt, cleanupWorkerOpt := newWorkerOpt(t, sb.ContainerdAddress())
	defer cleanupWorkerOpt()
	w, err := base.NewWorker(context.TODO(), workerOpt)
	require.NoError(t, err)

	tests.TestWorkerExec(t, w)
}

func testContainerdWorkerExecFailures(t *testing.T, sb integration.Sandbox) {
	workerOpt, cleanupWorkerOpt := newWorkerOpt(t, sb.ContainerdAddress())
	defer cleanupWorkerOpt()
	w, err := base.NewWorker(context.TODO(), workerOpt)
	require.NoError(t, err)

	tests.TestWorkerExecFailures(t, w)
}
