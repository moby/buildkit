package tests

import (
	"bytes"
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/executor"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/snapshot"
	"github.com/moby/buildkit/source/containerimage"
	"github.com/moby/buildkit/util/iohelper"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/moby/buildkit/worker/base"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

var mirrorOnce sync.Once
var mirror *integration.Mirror
var mirrorMu sync.Mutex

func RunMirror() func() error {
	mirrorOnce.Do(func() {
		m, err := integration.RunMirror()
		if err != nil {
			panic(err)
		}
		mirror = m
	})
	return func() error { return mirror.Close() }
}

func mirrorBusybox(t *testing.T) string {
	mirrorMu.Lock()
	defer mirrorMu.Unlock()
	require.NotNil(t, mirror, "mirror must be initialized")
	require.NoError(t, mirror.AddImages(t, integration.OfficialImages("busybox:latest")))
	return mirror.Host + "/library/busybox:latest"
}

func NewBusyboxSourceSnapshot(ctx context.Context, t *testing.T, w *base.Worker, sm *session.Manager) cache.ImmutableRef {
	img, err := containerimage.NewImageIdentifier(mirrorBusybox(t))
	require.NoError(t, err)
	src, err := w.SourceManager.Resolve(ctx, img, sm, nil)
	require.NoError(t, err)
	_, _, _, _, err = src.CacheKey(ctx, nil, 0)
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
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(errors.WithStack(context.Canceled))
	sm, err := session.NewManager()
	require.NoError(t, err)

	snap := NewBusyboxSourceSnapshot(ctx, t, w, sm)
	root, err := w.CacheMgr.New(ctx, snap, nil)
	require.NoError(t, err)

	id := identity.NewID()

	// verify pid1 exits when stdin sees EOF
	ctxTimeout, cancelTimeout := context.WithTimeoutCause(ctx, 5*time.Second, nil)
	started := make(chan struct{})
	pipeR, pipeW := io.Pipe()
	go func() {
		select {
		case <-ctxTimeout.Done():
			t.Error("Unexpected timeout waiting for pid1 to start")
		case <-started:
			pipeW.Write([]byte("hello"))
			pipeW.Close()
		}
	}()
	stdout := bytes.NewBuffer(nil)
	stderr := bytes.NewBuffer(nil)
	_, err = w.WorkerOpt.Executor.Run(ctxTimeout, id, execMount(root), nil, executor.ProcessInfo{
		Meta: executor.Meta{
			Args: []string{"cat"},
			Cwd:  "/",
			Env:  []string{"PATH=/bin:/usr/bin:/sbin:/usr/sbin"},
		},
		Stdin:  pipeR,
		Stdout: &iohelper.NopWriteCloser{Writer: stdout},
		Stderr: &iohelper.NopWriteCloser{Writer: stderr},
	}, started)
	cancelTimeout()
	t.Logf("Stdout: %s", stdout.String())
	t.Logf("Stderr: %s", stderr.String())
	require.NoError(t, err)
	require.Equal(t, "hello", stdout.String())
	require.Empty(t, stderr.String())

	// first start pid1 in the background
	execID := identity.NewID()
	eg := errgroup.Group{}
	started = make(chan struct{})
	pid1StdinR, pid1StdinW := io.Pipe()
	defer pid1StdinW.Close()
	eg.Go(func() error {
		_, err := w.WorkerOpt.Executor.Run(ctx, execID, execMount(root), nil, executor.ProcessInfo{
			Meta: executor.Meta{
				Args: []string{"cat"},
				Cwd:  "/",
				Env:  []string{"PATH=/bin:/usr/bin:/sbin:/usr/sbin"},
			},
			Stdin: pid1StdinR,
		}, started)
		return err
	})

	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Error("Unexpected timeout waiting for pid1 to start")
	}

	stdout.Reset()
	stderr.Reset()

	// verify pid1 is the cat command via Exec
	err = w.WorkerOpt.Executor.Exec(ctx, execID, executor.ProcessInfo{
		Meta: executor.Meta{
			Args: []string{"ps", "-o", "pid,comm"},
		},
		Stdout: &iohelper.NopWriteCloser{Writer: stdout},
		Stderr: &iohelper.NopWriteCloser{Writer: stderr},
	})
	t.Logf("Stdout: %s", stdout.String())
	t.Logf("Stderr: %s", stderr.String())
	require.NoError(t, err)
	// verify pid1 is cat
	require.Contains(t, stdout.String(), "1 cat")
	require.Empty(t, stderr.String())

	// simulate: echo -n "hello" | cat > /tmp/msg
	stdin := bytes.NewReader([]byte("hello"))
	stdout.Reset()
	stderr.Reset()
	err = w.WorkerOpt.Executor.Exec(ctx, execID, executor.ProcessInfo{
		Meta: executor.Meta{
			Args: []string{"sh", "-c", "cat > /tmp/msg"},
		},
		Stdin:  io.NopCloser(stdin),
		Stdout: &iohelper.NopWriteCloser{Writer: stdout},
		Stderr: &iohelper.NopWriteCloser{Writer: stderr},
	})
	require.NoError(t, err)
	require.Empty(t, stdout.String())
	require.Empty(t, stderr.String())

	// verify contents of /tmp/msg
	stdout.Reset()
	stderr.Reset()
	err = w.WorkerOpt.Executor.Exec(ctx, execID, executor.ProcessInfo{
		Meta: executor.Meta{
			Args: []string{"cat", "/tmp/msg"},
		},
		Stdout: &iohelper.NopWriteCloser{Writer: stdout},
		Stderr: &iohelper.NopWriteCloser{Writer: stderr},
	})
	t.Logf("Stdout: %s", stdout.String())
	t.Logf("Stderr: %s", stderr.String())
	require.NoError(t, err)
	require.Equal(t, "hello", stdout.String())
	require.Empty(t, stderr.String())

	// stop pid1
	require.NoError(t, pid1StdinW.Close())

	waitCh := make(chan error, 1)
	go func() {
		waitCh <- eg.Wait()
	}()
	select {
	case err = <-waitCh:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		cancel(errors.WithStack(context.Canceled))
		select {
		case err = <-waitCh:
			require.Failf(t, "timed out waiting for pid1 to exit", "pid1 returned after cancellation: %+v", err)
		case <-time.After(5 * time.Second):
			require.FailNow(t, "timed out waiting for pid1 to exit after cancellation")
		}
	}

	err = snap.Release(ctx)
	require.NoError(t, err)
}

func TestWorkerExecFailures(t *testing.T, w *base.Worker) {
	ctx := NewCtx("buildkit-test")
	sm, err := session.NewManager()
	require.NoError(t, err)

	snap := NewBusyboxSourceSnapshot(ctx, t, w, sm)
	root, err := w.CacheMgr.New(ctx, snap, nil)
	require.NoError(t, err)

	id := identity.NewID()

	// pid1 will start but only long enough for /bin/false to run
	eg := errgroup.Group{}
	started := make(chan struct{})
	eg.Go(func() error {
		_, err := w.WorkerOpt.Executor.Run(ctx, id, execMount(root), nil, executor.ProcessInfo{
			Meta: executor.Meta{
				Args: []string{"/bin/false"},
				Cwd:  "/",
			},
		}, started)
		return err
	})

	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Error("Unexpected timeout waiting for pid1 to start")
	}

	// this should fail since pid1 has already exited
	err = w.WorkerOpt.Executor.Exec(ctx, id, executor.ProcessInfo{
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
		_, err := w.WorkerOpt.Executor.Run(ctx, id, execMount(root), nil, executor.ProcessInfo{
			Meta: executor.Meta{
				Args: []string{"bogus"},
			},
		}, started)
		return err
	})

	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Error("Unexpected timeout waiting for pid1 to start")
	}

	// this should fail since pid1 never started
	err = w.WorkerOpt.Executor.Exec(ctx, id, executor.ProcessInfo{
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

func TestWorkerCancel(t *testing.T, w *base.Worker) {
	ctx := NewCtx("buildkit-test")
	sm, err := session.NewManager()
	require.NoError(t, err)

	snap := NewBusyboxSourceSnapshot(ctx, t, w, sm)
	root, err := w.CacheMgr.New(ctx, snap, nil)
	require.NoError(t, err)

	id := identity.NewID()

	started := make(chan struct{})

	pid1Ctx, pid1Cancel := context.WithCancelCause(ctx)
	defer pid1Cancel(errors.WithStack(context.Canceled))

	var (
		pid1Err, pid2Err error
		pid1Done         = make(chan struct{})
		pid2Done         = make(chan struct{})
	)

	go func() {
		defer close(pid1Done)
		_, pid1Err = w.WorkerOpt.Executor.Run(pid1Ctx, id, execMount(root), nil, executor.ProcessInfo{
			Meta: executor.Meta{
				Args: []string{"/bin/sleep", "10"},
				Cwd:  "/",
			},
		}, started)
	}()

	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Error("Unexpected timeout waiting for pid1 to start")
	}

	pid2Ctx, pid2Cancel := context.WithCancelCause(ctx)
	defer pid2Cancel(errors.WithStack(context.Canceled))

	started = make(chan struct{})

	go func() {
		defer close(pid2Done)
		// TODO why doesn't Exec allow for started channel??  Fake it for now
		go func() {
			<-time.After(2 * time.Second)
			close(started)
		}()
		pid2Err = w.WorkerOpt.Executor.Exec(pid2Ctx, id, executor.ProcessInfo{
			Meta: executor.Meta{
				Args: []string{"/bin/sleep", "10"},
				Cwd:  "/",
			},
		})
	}()

	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Error("Unexpected timeout waiting for pid2 to start")
	}

	pid2Cancel(errors.WithStack(context.Canceled))
	<-pid2Done
	require.Contains(t, pid2Err.Error(), "exit code: 137", "pid2 exits with sigkill")

	pid1Cancel(errors.WithStack(context.Canceled))
	<-pid1Done
	require.Contains(t, pid1Err.Error(), "exit code: 137", "pid1 exits with sigkill")
}

func execMount(m cache.Mountable) executor.Mount {
	return executor.Mount{Src: &mountable{m: m}}
}

type mountable struct {
	m cache.Mountable
}

func (m *mountable) Mount(ctx context.Context, readonly bool) (snapshot.Mountable, error) {
	return m.m.Mount(ctx, readonly, nil)
}
