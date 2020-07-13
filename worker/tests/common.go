package tests

import (
	"bytes"
	"context"
	"io"
	"io/ioutil"
	"testing"
	"time"

	"github.com/containerd/containerd/namespaces"
	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/executor"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/source"
	"github.com/moby/buildkit/worker/base"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

func NewBusyboxSourceSnapshot(ctx context.Context, t *testing.T, w *base.Worker, sm *session.Manager) cache.ImmutableRef {
	img, err := source.NewImageIdentifier("docker.io/library/busybox:latest")
	require.NoError(t, err)
	src, err := w.SourceManager.Resolve(ctx, img, sm)
	require.NoError(t, err)
	snap, err := src.Snapshot(ctx, nil)
	require.NoError(t, err)
	return snap
}

func NewCtx(s string) context.Context {
	return namespaces.WithNamespace(context.Background(), s)
}

func TestWorkerExec(t *testing.T, w *base.Worker) {
	ctx := NewCtx("buildkit-test")
	ctx, cancel := context.WithCancel(ctx)
	sm, err := session.NewManager()
	require.NoError(t, err)

	snap := NewBusyboxSourceSnapshot(ctx, t, w, sm)
	root, err := w.CacheManager.New(ctx, snap)
	require.NoError(t, err)

	id := identity.NewID()

	// first start pid1 in the background
	eg := errgroup.Group{}
	started := make(chan struct{})
	eg.Go(func() error {
		return w.Executor.Run(ctx, id, root, nil, executor.ProcessInfo{
			Meta: executor.Meta{
				Args: []string{"sleep", "10"},
				Cwd:  "/",
				Env:  []string{"PATH=/bin:/usr/bin:/sbin:/usr/sbin"},
			},
		}, started)
	})

	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Error("Unexpected timeout waiting for pid1 to start")
	}

	stdout := bytes.NewBuffer(nil)
	stderr := bytes.NewBuffer(nil)

	// verify pid1 is the sleep command via Exec
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

	// simulate: echo -n "hello" | cat > /tmp/msg
	stdin := bytes.NewReader([]byte("hello"))
	stdout.Reset()
	stderr.Reset()
	err = w.Executor.Exec(ctx, id, executor.ProcessInfo{
		Meta: executor.Meta{
			Args: []string{"sh", "-c", "cat > /tmp/msg"},
		},
		Stdin:  ioutil.NopCloser(stdin),
		Stdout: &nopCloser{stdout},
		Stderr: &nopCloser{stderr},
	})
	require.NoError(t, err)
	require.Empty(t, stdout.String())
	require.Empty(t, stderr.String())

	// verify contents of /tmp/msg
	stdout.Reset()
	stderr.Reset()
	err = w.Executor.Exec(ctx, id, executor.ProcessInfo{
		Meta: executor.Meta{
			Args: []string{"cat", "/tmp/msg"},
		},
		Stdout: &nopCloser{stdout},
		Stderr: &nopCloser{stderr},
	})
	t.Logf("Stdout: %s", stdout.String())
	t.Logf("Stderr: %s", stderr.String())
	require.NoError(t, err)
	require.Equal(t, "hello", stdout.String())
	require.Empty(t, stderr.String())

	// stop pid1
	cancel()

	err = eg.Wait()
	// we expect pid1 to get canceled after we test the exec
	require.EqualError(t, errors.Cause(err), context.Canceled.Error())

	err = snap.Release(ctx)
	require.NoError(t, err)
}

func TestWorkerExecFailures(t *testing.T, w *base.Worker) {
	ctx := NewCtx("buildkit-test")
	sm, err := session.NewManager()
	require.NoError(t, err)

	snap := NewBusyboxSourceSnapshot(ctx, t, w, sm)
	root, err := w.CacheManager.New(ctx, snap)
	require.NoError(t, err)

	id := identity.NewID()

	// pid1 will start but only long enough for /bin/false to run
	eg := errgroup.Group{}
	started := make(chan struct{})
	eg.Go(func() error {
		return w.Executor.Run(ctx, id, root, nil, executor.ProcessInfo{
			Meta: executor.Meta{
				Args: []string{"/bin/false"},
				Cwd:  "/",
			},
		}, started)
	})

	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Error("Unexpected timeout waiting for pid1 to start")
	}

	// this should fail since pid1 has already exited
	err = w.Executor.Exec(ctx, id, executor.ProcessInfo{
		Meta: executor.Meta{
			Args: []string{"/bin/true"},
		},
	})
	require.Error(t, err) // pid1 no longer running

	err = eg.Wait()
	require.Error(t, err) // process returned non-zero exit code: 1

	// pid1 will not start, bogus pid1 command
	eg = errgroup.Group{}
	started = make(chan struct{})
	eg.Go(func() error {
		return w.Executor.Run(ctx, id, root, nil, executor.ProcessInfo{
			Meta: executor.Meta{
				Args: []string{"bogus"},
			},
		}, started)
	})

	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Error("Unexpected timeout waiting for pid1 to start")
	}

	// this should fail since pid1 never started
	err = w.Executor.Exec(ctx, id, executor.ProcessInfo{
		Meta: executor.Meta{
			Args: []string{"/bin/true"},
		},
	})
	require.Error(t, err) // container has exited with error

	err = eg.Wait()
	require.Error(t, err) // pid1 did not terminate successfully

	err = snap.Release(ctx)
	require.NoError(t, err)
}

type nopCloser struct {
	io.Writer
}

func (n *nopCloser) Close() error {
	return nil
}
