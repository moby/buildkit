// +build linux,!no_containerd_worker

package containerd

import (
	"bytes"
	"context"
	"io"
	"io/ioutil"
	"os"
	"testing"

	"github.com/containerd/containerd/namespaces"
	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/executor"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/source"
	"github.com/moby/buildkit/util/network/netproviders"
	"github.com/moby/buildkit/worker/base"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

const sockFile = "/run/containerd/containerd.sock"
const ns = "buildkit-test"

func newWorkerOpt(t *testing.T) (base.WorkerOpt, func()) {
	tmpdir, err := ioutil.TempDir("", "workertest")
	require.NoError(t, err)
	cleanup := func() { os.RemoveAll(tmpdir) }
	workerOpt, err := NewWorkerOpt(tmpdir, sockFile, "overlayfs", ns, nil, nil, netproviders.Opt{Mode: "host"})
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

func newCtx(s string) context.Context {
	return namespaces.WithNamespace(context.Background(), s)
}

func newBusyboxSourceSnapshot(ctx context.Context, t *testing.T, w *base.Worker, sm *session.Manager) cache.ImmutableRef {
	img, err := source.NewImageIdentifier("docker.io/library/busybox:latest")
	require.NoError(t, err)
	src, err := w.SourceManager.Resolve(ctx, img, sm)
	require.NoError(t, err)
	snap, err := src.Snapshot(ctx, nil)
	require.NoError(t, err)
	return snap
}

func TestContainerdWorkerExec(t *testing.T) {
	t.Parallel()
	checkRequirement(t)

	workerOpt, cleanupWorkerOpt := newWorkerOpt(t)
	defer cleanupWorkerOpt()
	w, err := base.NewWorker(workerOpt)
	require.NoError(t, err)

	ctx := newCtx(ns)
	ctx, cancel := context.WithCancel(ctx)
	sm, err := session.NewManager()
	require.NoError(t, err)

	snap := newBusyboxSourceSnapshot(ctx, t, w, sm)
	root, err := w.CacheManager.New(ctx, snap)
	require.NoError(t, err)

	id := identity.NewID()

	// first start pid1 in the background
	eg := errgroup.Group{}
	eg.Go(func() error {
		return w.Executor.Run(ctx, id, root, nil, executor.ProcessInfo{
			Meta: executor.Meta{
				Args: []string{"sleep", "10"},
				Cwd:  "/",
				Env:  []string{"PATH=/bin:/usr/bin:/sbin:/usr/sbin"},
			},
		})
	})

	stdout := bytes.NewBuffer(nil)
	stderr := bytes.NewBuffer(nil)
	err = w.Executor.Exec(ctx, id, executor.ProcessInfo{
		Meta: executor.Meta{
			Args: []string{"ps", "-o", "pid,comm"},
		},
		Stdout: &nopCloser{stdout},
		Stderr: &nopCloser{stderr},
	})
	t.Logf("Stdout: %s", stdout.String())
	t.Logf("Stderr: %s", stderr.String())
	require.NoError(t, err)
	// verify pid1 is sleep
	require.Contains(t, stdout.String(), "1 sleep")
	require.Empty(t, stderr.String())

	// stop pid1
	cancel()

	err = eg.Wait()
	// we expect this to get canceled after we test the exec
	require.EqualError(t, errors.Cause(err), "context canceled")

	err = snap.Release(ctx)
	require.NoError(t, err)
}

type nopCloser struct {
	io.Writer
}

func (n *nopCloser) Close() error {
	return nil
}
