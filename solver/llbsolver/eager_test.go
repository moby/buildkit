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

	err := ep.wait(nil)
	assert.Equal(t, assert.AnError, err)
}

func TestEagerPipeline_WaitReturnsNilWhenNoError(t *testing.T) {
	ep := &eagerPipeline{
		compressWork: make(chan eagerWorkItem),
	}

	err := ep.wait(nil)
	assert.NoError(t, err)
}

func TestEagerPipeline_WaitDrainsLeftoverRefs(t *testing.T) {
	var released atomic.Int32
	ep := &eagerPipeline{
		compressWork: make(chan eagerWorkItem, 10),
	}

	ep.compressWork <- eagerWorkItem{ref: &releaseTracker{released: &released}}
	ep.compressWork <- eagerWorkItem{ref: &releaseTracker{released: &released}}

	err := ep.wait(nil)
	require.NoError(t, err)
	assert.Equal(t, int32(2), released.Load(), "leftover refs should be released by wait()")
}

func TestEagerPipeline_WaitIsIdempotent(t *testing.T) {
	ep := &eagerPipeline{
		compressWork: make(chan eagerWorkItem),
	}

	require.NoError(t, ep.wait(nil))
	require.NoError(t, ep.wait(nil), "second wait must not panic")
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

// Late fires of onVertexComplete must release the clone instead of sending
// into the (now closed) work channel.
func TestEagerPipeline_OnVertexCompleteAfterWait(t *testing.T) {
	var cloned atomic.Int32
	ep := &eagerPipeline{
		ctx:          context.Background(),
		compressWork: make(chan eagerWorkItem, 10),
		done:         make(chan struct{}),
	}
	require.NoError(t, ep.wait(nil))

	var released atomic.Int32
	res := newWorkerRefResult(&releaseTracker{released: &released, cloned: &cloned})

	require.NotPanics(t, func() {
		ep.onVertexComplete(nil, []solver.Result{res})
	})
	assert.Zero(t, cloned.Load())
	assert.Zero(t, released.Load())
}

// Many concurrent late fires after wait() must be rejected before cloning.
func TestEagerPipeline_OnVertexCompleteAfterWait_Concurrent(t *testing.T) {
	var cloned atomic.Int32
	ep := &eagerPipeline{
		ctx:          context.Background(),
		compressWork: make(chan eagerWorkItem, 10),
		done:         make(chan struct{}),
	}
	require.NoError(t, ep.wait(nil))

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

// A sender admitted before wait() but blocked on a full queue must take the
// done path, release its clone, and exit without panic.
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
		require.NoError(t, ep.wait(nil))
	})
	senderWg.Wait()

	assert.Equal(t, int32(2), released.Load())
}

// Fires racing concurrently with wait() must not panic and must not leak:
// every admitted clone is either drained by wait() or released by the sender.
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
		require.NoError(t, ep.wait(nil))
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
	ep := &eagerPipeline{}
	handler := func(_ context.Context, desc ocispecs.Descriptor) ([]ocispecs.Descriptor, error) {
		pushed = append(pushed, desc.Digest)
		return nil, nil
	}

	for _, desc := range descs {
		if !shouldEagerPushDesc(desc) {
			continue
		}
		err := ep.pushDescriptor(context.Background(), eagerPushItem{
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

// pushDescriptor must short-circuit on the second call for the same digest,
// without invoking the handler again. This is the fix for the noisy
// "eager pushing blob" log entries that appeared once per shared parent
// blob per ref.
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

	ep := &eagerPipeline{}
	for range 5 {
		require.NoError(t, ep.pushDescriptor(context.Background(), eagerPushItem{
			refID:   "ref-test",
			desc:    desc,
			handler: handler,
		}))
	}
	assert.Equal(t, int32(1), calls.Load(), "handler must run exactly once across repeated calls for the same digest")
}

// pushDescriptor must skip — without invoking the handler — when the
// keep-set is installed and the requesting refID is not in it.
func TestEagerPushDescriptor_SkipsNonKeptRef(t *testing.T) {
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

	ep := &eagerPipeline{}
	keep := map[string]struct{}{"final-ref": {}}
	ep.keepRefIDs.Store(&keep)

	require.NoError(t, ep.pushDescriptor(context.Background(), eagerPushItem{
		refID:   "intermediate-ref",
		desc:    desc,
		handler: handler,
	}))
	assert.Zero(t, calls.Load(), "non-kept ref must not invoke the push handler")

	// And the digest must still appear in the inflight tracker so
	// cancelNonKeptInflight can reason about it.
	v, ok := ep.inflight.Load(desc.Digest.String())
	require.True(t, ok)
	tracker := v.(*pushTracker)
	tracker.mu.Lock()
	_, hasRef := tracker.refIDs["intermediate-ref"]
	tracker.mu.Unlock()
	assert.True(t, hasRef, "tracker must record requester even when filtered")
}

// pushDescriptor must still push when at least one kept ref requests the
// digest, even if a non-kept ref also asked for it.
func TestEagerPushDescriptor_PushesWhenAnyRequesterKept(t *testing.T) {
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

	ep := &eagerPipeline{}
	keep := map[string]struct{}{"final-ref": {}}
	ep.keepRefIDs.Store(&keep)

	require.NoError(t, ep.pushDescriptor(context.Background(), eagerPushItem{
		refID:   "intermediate-ref",
		desc:    desc,
		handler: handler,
	}))
	require.NoError(t, ep.pushDescriptor(context.Background(), eagerPushItem{
		refID:   "final-ref",
		desc:    desc,
		handler: handler,
	}))
	assert.Equal(t, int32(1), calls.Load(), "kept-ref call must drive exactly one push")
}

// cancelNonKeptInflight must cancel the active push for a digest whose
// requesters are all non-kept, and must leave digests with at least one
// kept requester completely alone.
func TestEagerPipeline_CancelNonKeptInflight(t *testing.T) {
	ep := &eagerPipeline{ctx: context.Background()}

	keep := map[string]struct{}{"final-ref": {}}

	// Digest A: two non-kept requesters share one active push.
	var cancelA atomic.Bool
	ep.inflight.Store("digest-a", &pushTracker{
		refIDs:  map[string]struct{}{"int-ref-1": {}, "int-ref-2": {}},
		cancel:  func() { cancelA.Store(true) },
		pushing: true,
	})
	// Digest B: requested by both kept and non-kept — must be spared
	// even though it has an active push.
	var cancelB atomic.Bool
	ep.inflight.Store("digest-b", &pushTracker{
		refIDs:  map[string]struct{}{"int-ref-3": {}, "final-ref": {}},
		cancel:  func() { cancelB.Store(true) },
		pushing: true,
	})
	// Digest C: non-kept requester but no cancel yet (still queued).
	// Must not be counted and must not panic.
	ep.inflight.Store("digest-c", &pushTracker{
		refIDs: map[string]struct{}{"int-ref-4": {}},
	})

	n := ep.cancelNonKeptInflight(keep)
	assert.Equal(t, 1, n, "exactly one digest's active push should be cancelled")
	assert.True(t, cancelA.Load(), "digest A active push must be cancelled")
	assert.False(t, cancelB.Load(), "digest B must be spared (kept requester)")

	trackerA, _ := ep.inflight.Load("digest-a")
	assert.Nil(t, trackerA.(*pushTracker).cancel, "cancel should be cleared after cancellation")
}

// After cancelNonKeptInflight has fired, a kept ref that arrives later
// must not be skipped — it still needs to push the blob (the previous
// closure was cancelled before completion).
func TestEagerPushDescriptor_KeptRefAfterCancelStillPushes(t *testing.T) {
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

	ep := &eagerPipeline{}
	keep := map[string]struct{}{"final-ref": {}}
	ep.keepRefIDs.Store(&keep)

	// Simulate a prior cancel pass: tracker exists with the requester
	// recorded and no active push.
	ep.inflight.Store(desc.Digest.String(), &pushTracker{
		refIDs: map[string]struct{}{"int-ref": {}},
	})

	require.NoError(t, ep.pushDescriptor(context.Background(), eagerPushItem{
		refID:   "final-ref",
		desc:    desc,
		handler: handler,
	}))
	assert.Equal(t, int32(1), calls.Load(), "kept ref must drive a fresh push after a prior cancellation")
}

// releaseTracker is a minimal cache.ImmutableRef stub that counts
// Release calls. Clones share the counter.
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
