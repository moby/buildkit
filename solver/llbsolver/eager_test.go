package llbsolver

import (
	"context"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/containerd/containerd/v2/core/images"
	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/solver"
	"github.com/moby/buildkit/util/compression"
	"github.com/moby/buildkit/worker"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
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
	require.NoError(t, err)
	assert.Equal(t, int32(2), released.Load(), "leftover refs should be released by wait()")
}

func TestEagerPipeline_WaitIsIdempotent(t *testing.T) {
	ep := &eagerPipeline{
		work: make(chan eagerWorkItem),
	}

	require.NoError(t, ep.wait())
	require.NoError(t, ep.wait(), "second wait must not panic")
}

func TestEagerPipeline_WorkerExitsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	ep := &eagerPipeline{
		mode: EagerExportCompress,
		ctx:  ctx,
		work: make(chan eagerWorkItem, 10),
	}

	cancel(nil)

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

// Late fires of onVertexComplete must release the clone instead of sending
// into the (now closed) work channel.
func TestEagerPipeline_OnVertexCompleteAfterWait(t *testing.T) {
	ep := &eagerPipeline{
		ctx:  context.Background(),
		work: make(chan eagerWorkItem, 10),
	}
	require.NoError(t, ep.wait())

	var released atomic.Int32
	res := newWorkerRefResult(&releaseTracker{released: &released})

	require.NotPanics(t, func() {
		ep.onVertexComplete(nil, []solver.Result{res})
	})
	assert.Equal(t, int32(1), released.Load())
}

// Many concurrent late fires after wait() must all release cleanly.
func TestEagerPipeline_OnVertexCompleteAfterWait_Concurrent(t *testing.T) {
	ep := &eagerPipeline{
		ctx:  context.Background(),
		work: make(chan eagerWorkItem, 10),
	}
	require.NoError(t, ep.wait())

	var released atomic.Int32
	const fires = 100

	var wg sync.WaitGroup
	wg.Add(fires)
	for range fires {
		go func() {
			defer wg.Done()
			res := newWorkerRefResult(&releaseTracker{released: &released})
			ep.onVertexComplete(nil, []solver.Result{res})
		}()
	}
	wg.Wait()

	assert.Equal(t, int32(fires), released.Load())
}

// Fires racing concurrently with wait() must not panic and must not leak:
// every clone is either drained by wait() or released by the sender.
func TestEagerPipeline_OnVertexCompleteRacingWait(t *testing.T) {
	ep := &eagerPipeline{
		ctx:  context.Background(),
		work: make(chan eagerWorkItem, 256),
	}

	var released atomic.Int32
	const fires = 200

	var wg sync.WaitGroup
	wg.Add(fires)
	for range fires {
		go func() {
			defer wg.Done()
			res := newWorkerRefResult(&releaseTracker{released: &released})
			ep.onVertexComplete(nil, []solver.Result{res})
		}()
	}

	require.NotPanics(t, func() {
		require.NoError(t, ep.wait())
	})
	wg.Wait()

	assert.Equal(t, int32(fires), released.Load())
}

func TestEagerPushSkipsNonDistributableDescriptors(t *testing.T) {
	descs := []ocispecs.Descriptor{
		{
			Digest:    digest.FromString("push me"),
			MediaType: ocispecs.MediaTypeImageLayerGzip,
		},
		{
			Digest:    digest.FromString("skip me"),
			MediaType: ocispecs.MediaTypeImageLayerNonDistributableGzip, //nolint:staticcheck // deprecated but still supported
		},
		{
			Digest:    digest.FromString("push me too"),
			MediaType: images.MediaTypeDockerSchema2Layer,
		},
	}

	var pushed []digest.Digest
	ep := &eagerPipeline{}
	handler := func(_ context.Context, desc ocispecs.Descriptor) ([]ocispecs.Descriptor, error) {
		pushed = append(pushed, desc.Digest)
		return nil, nil
	}

	for _, desc := range descs {
		if !shouldEagerPushDesc(desc) {
			continue
		}
		err := ep.pushBlob(context.Background(), handler, desc)
		require.NoError(t, err)
	}

	assert.Equal(t, []digest.Digest{
		digest.FromString("push me"),
		digest.FromString("push me too"),
	}, pushed)
}

// releaseTracker is a minimal cache.ImmutableRef stub that counts
// Release calls. Clones share the counter.
type releaseTracker struct {
	cache.ImmutableRef
	released *atomic.Int32
}

func (r *releaseTracker) Release(context.Context) error {
	r.released.Add(1)
	return nil
}

func (r *releaseTracker) Clone() cache.ImmutableRef {
	return &releaseTracker{released: r.released}
}

func (r *releaseTracker) ID() string { return "release-tracker" }

type fakeWorker struct{ worker.Worker }

func (fakeWorker) ID() string { return "fake-worker" }

func newWorkerRefResult(ref cache.ImmutableRef) solver.Result {
	return worker.NewWorkerRefResult(ref, fakeWorker{})
}
