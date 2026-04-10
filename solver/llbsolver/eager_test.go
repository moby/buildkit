package llbsolver

import (
	"context"
	"os"
	"runtime"
	"sync/atomic"
	"testing"

	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/util/compression"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEagerWorkerCount_Default(t *testing.T) {
	os.Unsetenv("BUILDKIT_EAGER_EXPORT_WORKERS")
	n := eagerWorkerCount()
	assert.Equal(t, max(defaultEagerWorkers, runtime.NumCPU()), n)
}

func TestEagerWorkerCount_EnvOverride(t *testing.T) {
	t.Setenv("BUILDKIT_EAGER_EXPORT_WORKERS", "2")
	assert.Equal(t, 2, eagerWorkerCount())
}

func TestEagerWorkerCount_EnvInvalid(t *testing.T) {
	t.Setenv("BUILDKIT_EAGER_EXPORT_WORKERS", "not-a-number")
	assert.Equal(t, max(defaultEagerWorkers, runtime.NumCPU()), eagerWorkerCount())
}

func TestEagerWorkerCount_EnvZero(t *testing.T) {
	t.Setenv("BUILDKIT_EAGER_EXPORT_WORKERS", "0")
	assert.Equal(t, max(defaultEagerWorkers, runtime.NumCPU()), eagerWorkerCount())
}

func TestEagerWorkerCount_EnvNegative(t *testing.T) {
	t.Setenv("BUILDKIT_EAGER_EXPORT_WORKERS", "-1")
	assert.Equal(t, max(defaultEagerWorkers, runtime.NumCPU()), eagerWorkerCount())
}

func TestNewEagerPipeline_PushRequiresConfig(t *testing.T) {
	_, err := newEagerPipeline(context.Background(), EagerExportPush, compression.Config{}, "", nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "push config")
}

func TestEagerPipeline_WaitReturnsFirstError(t *testing.T) {
	ep := &eagerPipeline{
		work: make(chan eagerWorkItem),
	}
	ep.firstErr = assert.AnError

	err := ep.wait()
	assert.Equal(t, assert.AnError, err)
}

func TestEagerPipeline_WaitReturnsNilWhenNoError(t *testing.T) {
	ep := &eagerPipeline{
		work: make(chan eagerWorkItem),
	}

	err := ep.wait()
	assert.NoError(t, err)
}

func TestEagerPipeline_WaitDrainsLeftoverRefs(t *testing.T) {
	var released atomic.Int32
	ep := &eagerPipeline{
		work: make(chan eagerWorkItem, 10),
	}

	ep.work <- eagerWorkItem{ref: &releaseTracker{released: &released}}
	ep.work <- eagerWorkItem{ref: &releaseTracker{released: &released}}

	err := ep.wait()
	assert.NoError(t, err)
	assert.Equal(t, int32(2), released.Load(), "leftover refs should be released by wait()")
}

func TestEagerPipeline_WorkerExitsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ep := &eagerPipeline{
		mode: EagerExportCompress,
		ctx:  ctx,
		work: make(chan eagerWorkItem, 10),
	}

	cancel()

	ep.wg.Add(1)
	go ep.worker()
	ep.wg.Wait()
}

func TestEagerPipeline_WorkerExitsOnChannelClose(t *testing.T) {
	ep := &eagerPipeline{
		mode: EagerExportCompress,
		ctx:  context.Background(),
		work: make(chan eagerWorkItem),
	}

	ep.wg.Add(1)
	go ep.worker()

	close(ep.work)
	ep.wg.Wait()
}

// releaseTracker is a minimal stub that satisfies cache.ImmutableRef
// for testing ref lifecycle (Release calls). All other methods panic.
type releaseTracker struct {
	cache.ImmutableRef
	released *atomic.Int32
}

func (r *releaseTracker) Release(context.Context) error {
	r.released.Add(1)
	return nil
}
