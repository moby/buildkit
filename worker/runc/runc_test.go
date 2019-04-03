// +build linux,!no_runc_worker

package runc

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/containerd/containerd/namespaces"
	ctdsnapshot "github.com/containerd/containerd/snapshots"
	"github.com/containerd/containerd/snapshots/overlay"
	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/executor"
	"github.com/moby/buildkit/executor/oci"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/snapshot"
	"github.com/moby/buildkit/source"
	"github.com/moby/buildkit/worker/base"
	"github.com/stretchr/testify/require"
)

func newWorkerOpt(t *testing.T, processMode oci.ProcessMode) (base.WorkerOpt, func()) {
	tmpdir, err := ioutil.TempDir("", "workertest")
	require.NoError(t, err)
	cleanup := func() { os.RemoveAll(tmpdir) }

	snFactory := SnapshotterFactory{
		Name: "overlayfs",
		New: func(root string) (ctdsnapshot.Snapshotter, error) {
			return overlay.NewSnapshotter(root)
		},
	}
	rootless := false
	workerOpt, err := NewWorkerOpt(tmpdir, snFactory, rootless, processMode, nil, nil)
	require.NoError(t, err)

	return workerOpt, cleanup
}

func checkRequirement(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("requires root")
	}

	if _, err := exec.LookPath("runc"); err != nil {
		if _, err := exec.LookPath("buildkit-runc"); err != nil {
			t.Skipf("no runc found: %s", err.Error())
		}
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
	snap, err := src.Snapshot(ctx)
	require.NoError(t, err)
	return snap
}

func TestRuncWorker(t *testing.T) {
	t.Parallel()
	checkRequirement(t)

	workerOpt, cleanupWorkerOpt := newWorkerOpt(t, oci.ProcessSandbox)
	defer cleanupWorkerOpt()
	w, err := base.NewWorker(workerOpt)
	require.NoError(t, err)

	ctx := newCtx("buildkit-test")
	sm, err := session.NewManager()
	require.NoError(t, err)
	snap := newBusyboxSourceSnapshot(ctx, t, w, sm)

	mounts, err := snap.Mount(ctx, false)
	require.NoError(t, err)

	lm := snapshot.LocalMounter(mounts)

	target, err := lm.Mount()
	require.NoError(t, err)

	f, err := os.Open(target)
	require.NoError(t, err)

	names, err := f.Readdirnames(-1)
	require.NoError(t, err)
	require.True(t, len(names) > 5)

	err = f.Close()
	require.NoError(t, err)

	lm.Unmount()
	require.NoError(t, err)

	du, err := w.CacheManager.DiskUsage(ctx, client.DiskUsageInfo{})
	require.NoError(t, err)

	// for _, d := range du {
	// 	fmt.Printf("du: %+v\n", d)
	// }

	for _, d := range du {
		require.True(t, d.Size >= 8192)
	}

	meta := executor.Meta{
		Args: []string{"/bin/sh", "-c", "mkdir /run && echo \"foo\" > /run/bar"},
		Cwd:  "/",
	}

	stderr := bytes.NewBuffer(nil)
	err = w.Executor.Exec(ctx, meta, snap, nil, nil, nil, &nopCloser{stderr})
	require.Error(t, err) // Read-only root
	// typical error is like `mkdir /.../rootfs/proc: read-only file system`.
	// make sure the error is caused before running `echo foo > /bar`.
	require.Contains(t, stderr.String(), "read-only file system")

	root, err := w.CacheManager.New(ctx, snap)
	require.NoError(t, err)

	err = w.Executor.Exec(ctx, meta, root, nil, nil, nil, &nopCloser{stderr})
	require.NoError(t, err)

	meta = executor.Meta{
		Args: []string{"/bin/ls", "/etc/resolv.conf"},
		Cwd:  "/",
	}

	err = w.Executor.Exec(ctx, meta, root, nil, nil, nil, &nopCloser{stderr})
	require.NoError(t, err)

	rf, err := root.Commit(ctx)
	require.NoError(t, err)

	mounts, err = rf.Mount(ctx, false)
	require.NoError(t, err)

	lm = snapshot.LocalMounter(mounts)

	target, err = lm.Mount()
	require.NoError(t, err)

	//Verifies fix for issue https://github.com/moby/buildkit/issues/429
	dt, err := ioutil.ReadFile(filepath.Join(target, "run", "bar"))

	require.NoError(t, err)
	require.Equal(t, string(dt), "foo\n")

	lm.Unmount()
	require.NoError(t, err)

	err = rf.Release(ctx)
	require.NoError(t, err)

	err = snap.Release(ctx)
	require.NoError(t, err)

	du2, err := w.CacheManager.DiskUsage(ctx, client.DiskUsageInfo{})
	require.NoError(t, err)
	require.Equal(t, 1, len(du2)-len(du))

}

func TestRuncWorkerNoProcessSandbox(t *testing.T) {
	t.Parallel()
	checkRequirement(t)

	workerOpt, cleanupWorkerOpt := newWorkerOpt(t, oci.NoProcessSandbox)
	defer cleanupWorkerOpt()
	w, err := base.NewWorker(workerOpt)
	require.NoError(t, err)

	ctx := newCtx("buildkit-test")
	sm, err := session.NewManager()
	require.NoError(t, err)
	snap := newBusyboxSourceSnapshot(ctx, t, w, sm)
	root, err := w.CacheManager.New(ctx, snap)
	require.NoError(t, err)

	// ensure the procfs is shared
	selfPID := os.Getpid()
	selfCmdline, err := ioutil.ReadFile(fmt.Sprintf("/proc/%d/cmdline", selfPID))
	require.NoError(t, err)
	meta := executor.Meta{
		Args: []string{"/bin/cat", fmt.Sprintf("/proc/%d/cmdline", selfPID)},
		Cwd:  "/",
	}
	stdout := bytes.NewBuffer(nil)
	stderr := bytes.NewBuffer(nil)
	err = w.Executor.Exec(ctx, meta, root, nil, nil, &nopCloser{stdout}, &nopCloser{stderr})
	require.NoError(t, err, fmt.Sprintf("stdout=%q, stderr=%q", stdout.String(), stderr.String()))
	require.Equal(t, string(selfCmdline), stdout.String())
}

type nopCloser struct {
	io.Writer
}

func (n *nopCloser) Close() error {
	return nil
}
