// +build linux,!no_containerd_worker

package containerd

import (
	"io/ioutil"
	"os"
	"testing"

	"github.com/moby/buildkit/util/network/netproviders"
	"github.com/moby/buildkit/worker/base"
	"github.com/moby/buildkit/worker/tests"
	"github.com/stretchr/testify/require"
)

const sockFile = "/run/containerd/containerd.sock"

func newWorkerOpt(t *testing.T) (base.WorkerOpt, func()) {
	tmpdir, err := ioutil.TempDir("", "workertest")
	require.NoError(t, err)
	cleanup := func() { os.RemoveAll(tmpdir) }
	workerOpt, err := NewWorkerOpt(tmpdir, sockFile, "overlayfs", "buildkit-test", nil, nil, netproviders.Opt{Mode: "host"})
	require.NoError(t, err)
	return workerOpt, cleanup
}

func checkRequirement(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("requires root")
	}

	fi, err := os.Stat(sockFile)
	if err != nil {
		t.Skipf("Failed to stat %s: %s", sockFile, err.Error())
	}
	if fi.Mode()&os.ModeSocket == 0 {
		t.Skipf("%s is not a unix domain socket", sockFile)
	}
}

func TestContainerdWorkerExec(t *testing.T) {
	t.Parallel()
	checkRequirement(t)

	workerOpt, cleanupWorkerOpt := newWorkerOpt(t)
	defer cleanupWorkerOpt()
	w, err := base.NewWorker(workerOpt)
	require.NoError(t, err)

	tests.TestWorkerExec(t, w)
}
func TestContainerdWorkerExecFailures(t *testing.T) {
	t.Parallel()
	checkRequirement(t)

	workerOpt, cleanupWorkerOpt := newWorkerOpt(t)
	defer cleanupWorkerOpt()
	w, err := base.NewWorker(workerOpt)
	require.NoError(t, err)

	tests.TestWorkerExecFailures(t, w)
}
