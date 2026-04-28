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

func TestEagerPushWorkerCount_Default(t *testing.T) {
	os.Unsetenv("BUILDKIT_EAGER_PUSH_WORKERS")
	assert.Equal(t, max(defaultEagerPushWorkers, runtime.NumCPU()), eagerPushWorkerCount())
}

func TestEagerPushWorkerCount_EnvOverride(t *testing.T) {
	t.Setenv("BUILDKIT_EAGER_PUSH_WORKERS", "5")
	assert.Equal(t, 5, eagerPushWorkerCount())
}

func TestNewEagerPipeline_PushRequiresConfig(t *testing.T) {
	_, err := newEagerPipeline(context.Background(), EagerExportPush, compression.Config{}, "", nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "push config")
}

func TestEagerPipeline_WaitReturnsFirstError(t *testing.T) {
	ep := &eagerPipeline{
		compressWork: make(chan eagerWorkItem),
	}
	ep.firstErr = assert.AnError

	err := ep.wait()
	assert.Equal(t, assert.AnError, err)
}

func TestEagerPipeline_WaitReturnsNilWhenNoError(t *testing.T) {
	ep := &eagerPipeline{
		compressWork: make(chan eagerWorkItem),
	}

	err := ep.wait()
	assert.NoError(t, err)
}

func TestEagerPipeline_WaitDrainsLeftoverRefs(t *testing.T) {
	var released atomic.Int32
	ep := &eagerPipeline{
		compressWork: make(chan eagerWorkItem, 10),
	}

	ep.compressWork <- eagerWorkItem{ref: &releaseTracker{released: &released}}
	ep.compressWork <- eagerWorkItem{ref: &releaseTracker{released: &released}}

	err := ep.wait()
	require.NoError(t, err)
	assert.Equal(t, int32(2), released.Load(), "leftover refs should be released by wait()")
}

func TestEagerPipeline_WaitIsIdempotent(t *testing.T) {
	ep := &eagerPipeline{
		compressWork: make(chan eagerWorkItem),
	}

	require.NoError(t, ep.wait())
	require.NoError(t, ep.wait(), "second wait must not panic")
}

func TestEagerPipeline_CompressWorkerExitsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	ep := &eagerPipeline{
		mode:         EagerExportCompress,
		ctx:          ctx,
		compressWork: make(chan eagerWorkItem, 10),
	}

	cancel(nil)

	ep.compressWG.Add(1)
	go ep.compressWorker()
	ep.compressWG.Wait()
}

func TestEagerPipeline_CompressWorkerExitsOnChannelClose(t *testing.T) {
	ep := &eagerPipeline{
		mode:         EagerExportCompress,
		ctx:          context.Background(),
		compressWork: make(chan eagerWorkItem),
	}

	ep.compressWG.Add(1)
	go ep.compressWorker()

	close(ep.compressWork)
	ep.compressWG.Wait()
}

func TestEagerPipeline_PushWorkerExitsOnChannelClose(t *testing.T) {
	ep := &eagerPipeline{
		ctx:      context.Background(),
		pushWork: make(chan eagerPushItem),
	}

	ep.pushWG.Add(1)
	go ep.pushWorker()

	close(ep.pushWork)
	ep.pushWG.Wait()
}

func TestEagerPipeline_PushWorkerErrorReturnedByWait(t *testing.T) {
	ep := &eagerPipeline{
		ctx:          context.Background(),
		compressWork: make(chan eagerWorkItem),
		pushWork:     make(chan eagerPushItem, 1),
	}
	ep.pushWG.Add(1)
	go ep.pushWorker()

	resultCh := make(chan error, 1)
	ep.pushWork <- eagerPushItem{
		refID: "test-ref",
		desc: ocispecs.Descriptor{
			Digest:    digest.FromString("push-error"),
			MediaType: ocispecs.MediaTypeImageLayerGzip,
		},
		handler: func(context.Context, ocispecs.Descriptor) ([]ocispecs.Descriptor, error) {
			return nil, assert.AnError
		},
		result: resultCh,
	}

	require.Equal(t, assert.AnError, <-resultCh)
	require.Equal(t, assert.AnError, ep.wait())
}

// Late callbacks must be ignored after wait() starts shutdown.
func TestEagerPipeline_OnVertexCompleteAfterWait(t *testing.T) {
	var cloned atomic.Int32
	ep := &eagerPipeline{
		ctx:          context.Background(),
		compressWork: make(chan eagerWorkItem, 10),
		done:         make(chan struct{}),
	}
	require.NoError(t, ep.wait())

	var released atomic.Int32
	res := newWorkerRefResult(&releaseTracker{released: &released, cloned: &cloned})

	require.NotPanics(t, func() {
		ep.onVertexComplete(nil, []solver.Result{res})
	})
	assert.Zero(t, cloned.Load())
	assert.Zero(t, released.Load())
}

// Concurrent late callbacks must not clone refs.
func TestEagerPipeline_OnVertexCompleteAfterWait_Concurrent(t *testing.T) {
	var cloned atomic.Int32
	ep := &eagerPipeline{
		ctx:          context.Background(),
		compressWork: make(chan eagerWorkItem, 10),
		done:         make(chan struct{}),
	}
	require.NoError(t, ep.wait())

	var released atomic.Int32
	const fires = 100

	var wg sync.WaitGroup
	wg.Add(fires)
	for range fires {
		go func() {
			defer wg.Done()
			res := newWorkerRefResult(&releaseTracker{released: &released, cloned: &cloned})
			ep.onVertexComplete(nil, []solver.Result{res})
		}()
	}
	wg.Wait()

	assert.Zero(t, cloned.Load())
	assert.Zero(t, released.Load())
}

// Blocked senders must release clones when wait() closes the done channel.
func TestEagerPipeline_OnVertexCompleteBlockedSenderReleasedOnWait(t *testing.T) {
	var cloned atomic.Int32
	var released atomic.Int32
	ep := &eagerPipeline{
		ctx:          context.Background(),
		compressWork: make(chan eagerWorkItem, 1),
		done:         make(chan struct{}),
	}
	ep.compressWork <- eagerWorkItem{ref: &releaseTracker{released: &released}}

	res := newWorkerRefResult(&releaseTracker{released: &released, cloned: &cloned})

	var senderWg sync.WaitGroup
	senderWg.Add(1)
	go func() {
		defer senderWg.Done()
		ep.onVertexComplete(nil, []solver.Result{res})
	}()

	for range 1000 {
		if cloned.Load() == 1 {
			break
		}
		runtime.Gosched()
	}
	require.Equal(t, int32(1), cloned.Load())

	require.NotPanics(t, func() {
		require.NoError(t, ep.wait())
	})
	senderWg.Wait()

	assert.Equal(t, int32(2), released.Load())
}

// Callbacks racing with wait() must not panic or leak clones.
func TestEagerPipeline_OnVertexCompleteRacingWait(t *testing.T) {
	var cloned atomic.Int32
	ep := &eagerPipeline{
		ctx:          context.Background(),
		compressWork: make(chan eagerWorkItem, 256),
		done:         make(chan struct{}),
	}

	var released atomic.Int32
	const fires = 200

	var wg sync.WaitGroup
	wg.Add(fires)
	for range fires {
		go func() {
			defer wg.Done()
			res := newWorkerRefResult(&releaseTracker{released: &released, cloned: &cloned})
			ep.onVertexComplete(nil, []solver.Result{res})
		}()
	}

	require.NotPanics(t, func() {
		require.NoError(t, ep.wait())
	})
	wg.Wait()

	assert.Equal(t, cloned.Load(), released.Load())
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
	ep := &eagerPipeline{ctx: context.Background()}
	handler := func(_ context.Context, desc ocispecs.Descriptor) ([]ocispecs.Descriptor, error) {
		pushed = append(pushed, desc.Digest)
		return nil, nil
	}

	for _, desc := range descs {
		if !shouldEagerPushDesc(desc) {
			continue
		}
		err := ep.pushDescriptor(eagerPushItem{
			refID:   "test-ref",
			desc:    desc,
			handler: handler,
		})
		require.NoError(t, err)
	}

	assert.Equal(t, []digest.Digest{
		digest.FromString("push me"),
		digest.FromString("push me too"),
	}, pushed)
}

// Repeated pushes of the same digest should only call the handler once.
func TestEagerPushDescriptor_DedupsAfterSuccess(t *testing.T) {
	desc := ocispecs.Descriptor{
		Digest:    digest.FromString("shared-blob"),
		MediaType: ocispecs.MediaTypeImageLayerGzip,
		Size:      1234,
	}
	var calls atomic.Int32
	handler := func(_ context.Context, _ ocispecs.Descriptor) ([]ocispecs.Descriptor, error) {
		calls.Add(1)
		return nil, nil
	}

	ep := &eagerPipeline{ctx: context.Background()}
	for range 5 {
		require.NoError(t, ep.pushDescriptor(eagerPushItem{
			refID:   "ref-test",
			desc:    desc,
			handler: handler,
		}))
	}
	assert.Equal(t, int32(1), calls.Load(), "handler must run exactly once across repeated calls for the same digest")
}

func TestEagerPushDescriptor_DedupsConcurrentPushes(t *testing.T) {
	desc := ocispecs.Descriptor{
		Digest:    digest.FromString("shared-blob"),
		MediaType: ocispecs.MediaTypeImageLayerGzip,
		Size:      1234,
	}
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	handler := func(_ context.Context, _ ocispecs.Descriptor) ([]ocispecs.Descriptor, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return nil, nil
	}

	ep := &eagerPipeline{ctx: context.Background()}
	errCh := make(chan error, 2)
	go func() {
		errCh <- ep.pushDescriptor(eagerPushItem{
			refID:   "ref-a",
			desc:    desc,
			handler: handler,
		})
	}()
	<-started
	go func() {
		errCh <- ep.pushDescriptor(eagerPushItem{
			refID:   "ref-b",
			desc:    desc,
			handler: handler,
		})
	}()
	close(release)

	require.NoError(t, <-errCh)
	require.NoError(t, <-errCh)
	assert.Equal(t, int32(1), calls.Load(), "handler must run exactly once for concurrent pushes of the same digest")
}

// Non-export refs should be tracked but not pushed.
func TestEagerPushDescriptor_SkipsNonExportRef(t *testing.T) {
	desc := ocispecs.Descriptor{
		Digest:    digest.FromString("intermediate-blob"),
		MediaType: ocispecs.MediaTypeImageLayerGzip,
		Size:      4096,
	}
	var calls atomic.Int32
	handler := func(_ context.Context, _ ocispecs.Descriptor) ([]ocispecs.Descriptor, error) {
		calls.Add(1)
		return nil, nil
	}

	ep := &eagerPipeline{ctx: context.Background()}
	exportRefs := map[string]struct{}{"final-ref": {}}
	ep.exportRefIDs.Store(&exportRefs)

	require.NoError(t, ep.pushDescriptor(eagerPushItem{
		refID:   "intermediate-ref",
		desc:    desc,
		handler: handler,
	}))
	assert.Zero(t, calls.Load(), "non-export ref must not invoke the push handler")

	v, ok := ep.inflight.Load(desc.Digest.String())
	require.True(t, ok)
	tracker := v.(*pushTracker)
	tracker.mu.Lock()
	_, hasRef := tracker.refIDs["intermediate-ref"]
	tracker.mu.Unlock()
	assert.True(t, hasRef, "tracker must record requester even when filtered")
}

// An export requester should push even if the digest was first requested by a non-export ref.
func TestEagerPushDescriptor_PushesWhenAnyRequesterExported(t *testing.T) {
	desc := ocispecs.Descriptor{
		Digest:    digest.FromString("shared-blob"),
		MediaType: ocispecs.MediaTypeImageLayerGzip,
		Size:      4096,
	}
	var calls atomic.Int32
	handler := func(_ context.Context, _ ocispecs.Descriptor) ([]ocispecs.Descriptor, error) {
		calls.Add(1)
		return nil, nil
	}

	ep := &eagerPipeline{ctx: context.Background()}
	exportRefs := map[string]struct{}{"final-ref": {}}
	ep.exportRefIDs.Store(&exportRefs)

	require.NoError(t, ep.pushDescriptor(eagerPushItem{
		refID:   "intermediate-ref",
		desc:    desc,
		handler: handler,
	}))
	require.NoError(t, ep.pushDescriptor(eagerPushItem{
		refID:   "final-ref",
		desc:    desc,
		handler: handler,
	}))
	assert.Equal(t, int32(1), calls.Load(), "export-ref call must drive exactly one push")
}

// cancelNonExportInflight cancels only digests with no export requesters.
func TestEagerPipeline_CancelNonExportInflight(t *testing.T) {
	ep := &eagerPipeline{ctx: context.Background()}

	exportRefs := map[string]struct{}{"final-ref": {}}

	// Digest A: all requesters are non-export, so every waiter must cancel.
	var cancelA1, cancelA2 atomic.Bool
	ep.inflight.Store("digest-a", &pushTracker{
		refIDs: map[string]struct{}{"int-ref-1": {}, "int-ref-2": {}},
		cancels: map[string]context.CancelFunc{
			"int-ref-1": func() { cancelA1.Store(true) },
			"int-ref-2": func() { cancelA2.Store(true) },
		},
	})
	// Digest B: one export requester leaves the shared push alive.
	var cancelB atomic.Bool
	ep.inflight.Store("digest-b", &pushTracker{
		refIDs: map[string]struct{}{"int-ref-3": {}, "final-ref": {}},
		cancels: map[string]context.CancelFunc{
			"int-ref-3": func() { cancelB.Store(true) },
		},
	})
	// Digest C: queued work has no waiter to cancel.
	ep.inflight.Store("digest-c", &pushTracker{
		refIDs: map[string]struct{}{"int-ref-4": {}},
	})

	n := ep.cancelNonExportInflight(exportRefs)
	assert.Equal(t, 1, n, "exactly one digest's waiters should be cancelled")
	assert.True(t, cancelA1.Load(), "digest A waiter 1 must be cancelled")
	assert.True(t, cancelA2.Load(), "digest A waiter 2 must be cancelled")
	assert.False(t, cancelB.Load(), "digest B must be spared (export requester)")

	trackerA, _ := ep.inflight.Load("digest-a")
	assert.Empty(t, trackerA.(*pushTracker).cancels, "cancels map should be drained after cancellation")
}

// An export ref arriving after cancellation should start a fresh push.
func TestEagerPushDescriptor_ExportRefAfterCancelStillPushes(t *testing.T) {
	desc := ocispecs.Descriptor{
		Digest:    digest.FromString("retry-after-cancel"),
		MediaType: ocispecs.MediaTypeImageLayerGzip,
		Size:      4096,
	}
	var calls atomic.Int32
	handler := func(_ context.Context, _ ocispecs.Descriptor) ([]ocispecs.Descriptor, error) {
		calls.Add(1)
		return nil, nil
	}

	ep := &eagerPipeline{ctx: context.Background()}
	exportRefs := map[string]struct{}{"final-ref": {}}
	ep.exportRefIDs.Store(&exportRefs)

	// Simulate a prior cancel pass that already removed its waiter.
	ep.inflight.Store(desc.Digest.String(), &pushTracker{
		refIDs:  map[string]struct{}{"int-ref": {}},
		cancels: map[string]context.CancelFunc{},
	})

	require.NoError(t, ep.pushDescriptor(eagerPushItem{
		refID:   "final-ref",
		desc:    desc,
		handler: handler,
	}))
	assert.Equal(t, int32(1), calls.Load(), "export ref must drive a fresh push after a prior cancellation")
}

// releaseTracker counts Release calls across clones.
type releaseTracker struct {
	cache.ImmutableRef
	released *atomic.Int32
	cloned   *atomic.Int32
}

func (r *releaseTracker) Release(context.Context) error {
	r.released.Add(1)
	return nil
}

func (r *releaseTracker) Clone() cache.ImmutableRef {
	if r.cloned != nil {
		r.cloned.Add(1)
	}
	return &releaseTracker{released: r.released}
}

func (r *releaseTracker) ID() string { return "release-tracker" }

type fakeWorker struct{ worker.Worker }

func (fakeWorker) ID() string { return "fake-worker" }

func newWorkerRefResult(ref cache.ImmutableRef) solver.Result {
	return worker.NewWorkerRefResult(ref, fakeWorker{})
}
