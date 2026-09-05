package forwarder

import (
	"context"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/moby/buildkit/cache"
	buildkitclient "github.com/moby/buildkit/client"
	"github.com/moby/buildkit/executor"
	resourcestypes "github.com/moby/buildkit/executor/resources/types"
	"github.com/moby/buildkit/frontend/gateway/container"
	gwclient "github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/system"
	"github.com/stretchr/testify/require"
)

func TestNewContainerPreservesPlatform(t *testing.T) {
	exec := &recordingExecutor{process: make(chan executor.ProcessInfo, 1)}
	c := &BridgeClient{
		workers:  emptyWorkerInfos{},
		executor: exec,
	}
	targetOS := "windows"
	if runtime.GOOS == targetOS {
		targetOS = "linux"
	}
	ctx := t.Context()

	ctr, err := c.NewContainer(ctx, gwclient.NewContainerRequest{
		Platform: &pb.Platform{OS: targetOS, Architecture: "amd64"},
	})
	require.NoError(t, err)

	proc, err := ctr.Start(ctx, gwclient.StartRequest{})
	require.NoError(t, err)
	require.Contains(t, (<-exec.process).Meta.Env, "PATH="+system.DefaultPathEnv(targetOS))
	require.NoError(t, proc.Wait())
	require.NoError(t, ctr.Release(ctx))
}

type emptyWorkerInfos struct{}

func (emptyWorkerInfos) DefaultCacheManager() (cache.Manager, error) {
	return nil, nil
}

func (emptyWorkerInfos) WorkerInfos() []buildkitclient.WorkerInfo {
	return nil
}

type recordingExecutor struct {
	process chan executor.ProcessInfo
}

func (e *recordingExecutor) Run(_ context.Context, _ string, _ executor.Mount, _ []executor.Mount, process executor.ProcessInfo, started chan<- struct{}) (resourcestypes.Recorder, error) {
	e.process <- process
	close(started)
	return nil, nil
}

func (e *recordingExecutor) Exec(context.Context, string, executor.ProcessInfo) error {
	return nil
}

func TestDiscardWaitsForContainerCreation(t *testing.T) {
	containerCtx, cancelContainerCtx := context.WithCancelCause(t.Context())
	creationStarted := make(chan struct{})
	finishCreation := make(chan struct{})
	var finishOnce sync.Once
	finish := func() { finishOnce.Do(func() { close(finishCreation) }) }
	t.Cleanup(finish)
	ctr := &lifecycleTestContainer{released: make(chan struct{})}
	c := &BridgeClient{
		containerCtx:       containerCtx,
		cancelContainerCtx: cancelContainerCtx,
		newContainer: func(context.Context, container.NewContainerRequest) (gwclient.Container, error) {
			close(creationStarted)
			<-finishCreation
			return ctr, nil
		},
	}

	creationDone := make(chan error, 1)
	go func() {
		_, err := c.NewContainer(t.Context(), lifecycleContainerRequest())
		creationDone <- err
	}()
	receiveLifecycleTest(t, creationStarted, "container creation")

	discardDone := make(chan struct{})
	go func() {
		c.discard(nil)
		close(discardDone)
	}()

	require.Eventually(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.closing
	}, 5*time.Second, 10*time.Millisecond)
	select {
	case <-discardDone:
		t.Fatal("discard completed while container creation was in progress")
	default:
	}

	finish()
	require.NoError(t, receiveLifecycleTest(t, creationDone, "container creation completion"))
	receiveLifecycleTest(t, ctr.released, "container release")
	receiveLifecycleTest(t, discardDone, "discard")

	_, err := c.NewContainer(t.Context(), lifecycleContainerRequest())
	require.ErrorContains(t, err, "gateway client is closing")
}

func TestDiscardCancelsContainerCreation(t *testing.T) {
	containerCtx, cancelContainerCtx := context.WithCancelCause(t.Context())
	creationStarted := make(chan struct{})
	contextAtStart := make(chan error, 1)
	c := &BridgeClient{
		containerCtx:       containerCtx,
		cancelContainerCtx: cancelContainerCtx,
		newContainer: func(ctx context.Context, _ container.NewContainerRequest) (gwclient.Container, error) {
			close(creationStarted)
			contextAtStart <- context.Cause(ctx)
			<-ctx.Done()
			return nil, context.Cause(ctx)
		},
	}

	creationDone := make(chan error, 1)
	go func() {
		_, err := c.NewContainer(t.Context(), lifecycleContainerRequest())
		creationDone <- err
	}()
	receiveLifecycleTest(t, creationStarted, "container creation")
	require.NoError(t, receiveLifecycleTest(t, contextAtStart, "live container context"))

	discardDone := make(chan struct{})
	go func() {
		c.discard(nil)
		close(discardDone)
	}()
	require.ErrorIs(t, receiveLifecycleTest(t, creationDone, "container creation cancellation"), context.Canceled)
	receiveLifecycleTest(t, discardDone, "discard")
}

type lifecycleTestContainer struct {
	gwclient.Container
	released chan struct{}
	once     sync.Once
}

func (c *lifecycleTestContainer) Release(context.Context) error {
	c.once.Do(func() { close(c.released) })
	return nil
}

func lifecycleContainerRequest() gwclient.NewContainerRequest {
	return gwclient.NewContainerRequest{Mounts: []gwclient.Mount{{
		Dest:      pb.RootMount,
		MountType: pb.MountType_BIND,
	}}}
}

func receiveLifecycleTest[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		var zero T
		return zero
	}
}
