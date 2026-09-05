package container

import (
	"context"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/moby/buildkit/executor"
	resourcestypes "github.com/moby/buildkit/executor/resources/types"
	gwclient "github.com/moby/buildkit/frontend/gateway/client"
	opspb "github.com/moby/buildkit/solver/pb"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

type blockingExecutor struct {
	runStarted chan struct{}
	finishRun  chan struct{}
}

func (e *blockingExecutor) Run(ctx context.Context, _ string, _ executor.Mount, _ []executor.Mount, _ executor.ProcessInfo, _ chan<- struct{}) (resourcestypes.Recorder, error) {
	close(e.runStarted)
	<-ctx.Done()
	<-e.finishRun
	return nil, nil
}

func (e *blockingExecutor) Exec(context.Context, string, executor.ProcessInfo) error {
	return errors.New("unexpected exec")
}

func TestReleaseWaitsForConcurrentStart(t *testing.T) {
	runStarted := make(chan struct{})
	finishRun := make(chan struct{})
	cleanupStarted := make(chan struct{})
	var finishOnce sync.Once
	finish := func() {
		finishOnce.Do(func() { close(finishRun) })
	}
	t.Cleanup(finish)

	ctr := newLifecycleTestContainer(t, &blockingExecutor{
		runStarted: runStarted,
		finishRun:  finishRun,
	})
	ctr.cleanup = []func() error{func() error {
		close(cleanupStarted)
		return nil
	}}

	startDone := make(chan error, 1)
	go func() {
		proc, err := ctr.Start(t.Context(), gwclient.StartRequest{Args: []string{"true"}})
		if err == nil {
			err = proc.Wait()
		}
		startDone <- err
	}()
	receiveLifecycleTest(t, runStarted, "executor start")

	releaseDone := make(chan error, 1)
	go func() {
		releaseDone <- ctr.Release(t.Context())
	}()
	receiveLifecycleTest(t, ctr.ctx.Done(), "container cancellation")

	select {
	case <-cleanupStarted:
		t.Fatal("cleanup started while an admitted process was still running")
	case <-time.After(100 * time.Millisecond):
	}

	finish()
	require.NoError(t, receiveLifecycleTest(t, startDone, "process completion"))
	require.NoError(t, receiveLifecycleTest(t, releaseDone, "container release"))
}

func TestStartRejectsClosedContainer(t *testing.T) {
	finishRun := make(chan struct{})
	close(finishRun)
	exec := &blockingExecutor{
		runStarted: make(chan struct{}),
		finishRun:  finishRun,
	}
	ctr := newLifecycleTestContainer(t, exec)
	require.NoError(t, ctr.Release(t.Context()))

	_, err := ctr.Start(t.Context(), gwclient.StartRequest{Args: []string{"true"}})
	require.ErrorContains(t, err, "container is closed")
	select {
	case <-exec.runStarted:
		t.Fatal("executor started after container release")
	default:
	}
}

func newLifecycleTestContainer(t *testing.T, exec executor.Executor) *gatewayContainer {
	t.Helper()
	baseCtx, cancel := context.WithCancelCause(t.Context())
	eg, ctx := errgroup.WithContext(baseCtx)
	return &gatewayContainer{
		id:       "test",
		platform: &opspb.Platform{OS: runtime.GOOS, Architecture: runtime.GOARCH},
		executor: exec,
		errGroup: eg,
		ctx:      ctx,
		cancel:   cancel,
	}
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

var _ executor.Executor = (*blockingExecutor)(nil)
