package llbsolver

import (
	"context"
	"os"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/core/remotes"
	"github.com/moby/buildkit/cache"
	cacheconfig "github.com/moby/buildkit/cache/config"
	"github.com/moby/buildkit/exporter"
	"github.com/moby/buildkit/frontend"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/solver"
	"github.com/moby/buildkit/util/bklog"
	"github.com/moby/buildkit/util/compression"
	pushutil "github.com/moby/buildkit/util/push"
	"github.com/moby/buildkit/util/resolver/limited"
	"github.com/moby/buildkit/util/resolver/retryhandler"
	"github.com/moby/buildkit/util/tracing"
	"github.com/moby/buildkit/worker"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// Keep the pools large enough for a ref's descriptor chain to fan out without
// serializing pushes.
const (
	defaultEagerWorkers     = 128
	defaultEagerPushWorkers = 128
)

type eagerWorkItem struct {
	ref cache.ImmutableRef
}

// eagerPushItem is a single unique push descriptor handed to the push pool.
// Duplicate descriptor requests are tracked per digest and do not enqueue
// additional work.
type eagerPushItem struct {
	refID   string
	desc    ocispecs.Descriptor
	handler func(context.Context, ocispecs.Descriptor) ([]ocispecs.Descriptor, error)
	attempt uint64
}

// eagerPipeline manages background compression and pushing of layer blobs as
// build vertices complete, rather than deferring all work to finalize.
//
// It is split into two worker pools:
//   - Compress pool: receives refs from the solver vertex callback, runs
//     GetRemotes (which performs blob compression for the full parent chain),
//     and dispatches each pushable descriptor onto the push pool.
//   - Push pool: receives individual descriptors and uploads them to the
//     registry. Decoupling lets a single ref fan out parallel pushes across
//     its descriptor chain (instead of serializing them in one goroutine),
//     which matches the parallelism that vanilla buildkit gets from
//     images.Dispatch.
type eagerPipeline struct {
	mode      EagerExportMode
	refCfg    cacheconfig.RefConfig
	sessionID string
	pushCfg   *exporter.EagerPushConfig

	compressWork chan eagerWorkItem
	pushWork     chan eagerPushItem
	done         chan struct{}

	compressWG sync.WaitGroup
	pushWG     sync.WaitGroup

	// closeMu gates new senders from entering shutdown. Senders increment
	// senderWg while holding the mutex so wait() can stop admission before
	// waiting for all in-flight send attempts to finish.
	closeMu  sync.Mutex
	closing  bool
	senderWg sync.WaitGroup
	waitOnce sync.Once

	// ctx carries the lease so compressed blobs are GC-protected.
	ctx         context.Context
	cancelCause context.CancelCauseFunc

	// pusher is created at pipeline init when mode is EagerExportPush.
	pusher remotes.Pusher

	// keepRefIDs is the set of ImmutableRef.ID()s whose blobs are part of
	// the final exported image manifest. It is populated once, by wait(),
	// after the frontend has fully resolved its result. While nil, every
	// ref's blobs are eligible for compression+push. Once non-nil, processRef
	// and requestPushDescriptor skip refs
	// whose ID is not in the set, and wait() cancels in-flight
	// pushes whose only requesters are non-kept refs.
	keepRefIDs atomic.Pointer[map[string]struct{}]
	// inflight tracks per-digest cancel funcs and the set of refIDs that
	// requested each digest. Used by wait() to cancel uploads
	// whose every requester turns out to be non-kept.
	inflight sync.Map // map[string]*pushTracker

	mu       sync.Mutex
	firstErr error
}

// pushTracker holds bookkeeping for a single digest's eager push. A digest is
// enqueued at most once at a time; duplicate requesters are recorded here but do
// not occupy push worker slots.
type pushTracker struct {
	mu        sync.Mutex
	refIDs    map[string]struct{}
	enqueued  bool
	pushing   bool
	pushed    bool
	cancel    context.CancelFunc
	cancelled bool
	attempt   uint64
}

func eagerWorkerCount() int {
	return envWorkerCount("BUILDKIT_EAGER_EXPORT_WORKERS", defaultEagerWorkers)
}

func eagerPushWorkerCount() int {
	return envWorkerCount("BUILDKIT_EAGER_PUSH_WORKERS", defaultEagerPushWorkers)
}

func envWorkerCount(env string, fallback int) int {
	if s := os.Getenv(env); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			return n
		}
	}
	return max(fallback, runtime.NumCPU())
}

func newEagerPipeline(ctx context.Context, mode EagerExportMode, comp compression.Config, sessionID string, sm *session.Manager, pushCfg *exporter.EagerPushConfig) (*eagerPipeline, error) {
	if mode == EagerExportPush && pushCfg == nil {
		return nil, errors.New("eager-export=push requires push config")
	}

	var pusher remotes.Pusher
	if mode == EagerExportPush {
		var err error
		pusher, err = pushutil.NewPusher(ctx, sm, sessionID, pushCfg.TargetName, pushCfg.Insecure, pushCfg.RegistryHosts)
		if err != nil {
			return nil, errors.Wrap(err, "eager-export=push: failed to create pusher")
		}
	}

	pipelineCtx, cancel := context.WithCancelCause(ctx)
	ep := &eagerPipeline{
		mode: mode,
		refCfg: cacheconfig.RefConfig{
			Compression: comp,
		},
		sessionID:    sessionID,
		pushCfg:      pushCfg,
		pusher:       pusher,
		ctx:          pipelineCtx,
		cancelCause:  cancel,
		compressWork: make(chan eagerWorkItem, 256),
		pushWork:     make(chan eagerPushItem, 1024),
		done:         make(chan struct{}),
	}

	numCompress := eagerWorkerCount()
	ep.compressWG.Add(numCompress)
	for range numCompress {
		go ep.compressWorker()
	}

	if mode == EagerExportPush {
		numPush := eagerPushWorkerCount()
		ep.pushWG.Add(numPush)
		for range numPush {
			go ep.pushWorker()
		}
		bklog.G(ctx).Infof("eager pipeline started compress_workers=%d push_workers=%d", numCompress, numPush)
	} else {
		bklog.G(ctx).Infof("eager pipeline started compress_workers=%d (no push)", numCompress)
	}

	return ep, nil
}

func (ep *eagerPipeline) cancel(err error) {
	if ep.cancelCause != nil {
		ep.cancelCause(err)
	}
}

func (ep *eagerPipeline) compressWorker() {
	defer ep.compressWG.Done()
	for {
		select {
		case <-ep.ctx.Done():
			return
		case item, ok := <-ep.compressWork:
			if !ok {
				return
			}
			if err := ep.processRef(item.ref); err != nil {
				ep.recordErr(err)
			}
			item.ref.Release(context.TODO())
		}
	}
}

func (ep *eagerPipeline) pushWorker() {
	defer ep.pushWG.Done()
	for {
		// Don't bail out on ctx.Done here: wait() closes pushWork only after
		// compression has finished enqueueing, and the worker should drain any
		// remaining items so cancellation bookkeeping can settle.
		item, ok := <-ep.pushWork
		if !ok {
			return
		}
		if err := ep.pushDescriptor(ep.ctx, item); err != nil {
			ep.recordErr(err)
		}
	}
}

func (ep *eagerPipeline) recordErr(err error) {
	ep.mu.Lock()
	if ep.firstErr == nil {
		ep.firstErr = err
	}
	ep.mu.Unlock()
}

// onVertexComplete is the callback registered on the solver Job. It extracts
// ImmutableRefs from vertex results, clones them for safe async use, and
// sends them to the compress pool. Fires that arrive after shutdown starts
// are rejected before enqueue and release their clones.
func (ep *eagerPipeline) onVertexComplete(vtx solver.Vertex, results []solver.Result) {
	for _, res := range results {
		if res == nil {
			continue
		}
		workerRef, ok := res.Sys().(*worker.WorkerRef)
		if !ok || workerRef.ImmutableRef == nil {
			continue
		}

		ep.closeMu.Lock()
		if ep.closing {
			ep.closeMu.Unlock()
			continue
		}
		ep.senderWg.Add(1)
		ep.closeMu.Unlock()

		cloned := workerRef.ImmutableRef.Clone()
		select {
		case ep.compressWork <- eagerWorkItem{ref: cloned}:
			ep.senderWg.Done()
		case <-ep.done:
			cloned.Release(context.TODO())
			ep.senderWg.Done()
		case <-ep.ctx.Done():
			cloned.Release(context.TODO())
			ep.senderWg.Done()
			return
		}
	}
}

// computeEagerKeepSet walks the resolved frontend Result and returns the
// set of ImmutableRef.ID()s that belong to the final exported image —
// including every layer in each output ref's parent chain.
//
// Why the chain matters: each layer is its own ImmutableRef with its own
// ID, *equal to* the ID of the intermediate vertex that produced it. The
// solver's onVertexComplete fires once per vertex during the build, so
// the eager pipeline pushes layer L_n via vertex V_n's processRef long
// before V_final completes. If we only kept V_final.ID() we'd filter
// every intermediate vertex's processRef, and all the real layer pushes
// would get deferred to wait() — defeating the entire point of eager
// export.
//
// res must already be fully resolved: every ResultProxy.Result(ctx) call
// must have completed without error. The caller in solver.go ensures
// this via eg.Wait() right before invoking us.
//
// If res is nil or contains zero refs, returns an empty map (== filter
// everything). Errors resolving individual refs are logged but not
// returned: a partial keep-set is safer than failing the build, since a
// missing entry just costs some wasted bandwidth, not correctness.
func computeEagerKeepSet(ctx context.Context, res *frontend.Result) map[string]struct{} {
	keep := make(map[string]struct{})
	if res == nil {
		return keep
	}
	res.EachRef(func(rp solver.ResultProxy) error {
		if rp == nil {
			return nil
		}
		cached, err := rp.Result(ctx)
		if err != nil {
			bklog.G(ctx).WithError(err).Warnf("eager keep-set: failed to resolve ref")
			return nil
		}
		workerRef, ok := cached.Sys().(*worker.WorkerRef)
		if !ok || workerRef.ImmutableRef == nil {
			return nil
		}
		// LayerChain walks BaseLayer → ... → tip, including the tip
		// itself. Each entry is a *clone* of the underlying ref — we
		// only need its ID, then must release.
		chain := workerRef.ImmutableRef.LayerChain()
		for _, layer := range chain {
			if layer == nil {
				continue
			}
			keep[layer.ID()] = struct{}{}
		}
		if err := chain.Release(context.WithoutCancel(ctx)); err != nil {
			bklog.G(ctx).WithError(err).Warnf("eager keep-set: failed to release chain clones")
		}
		return nil
	})
	return keep
}

func computeEagerExportRefs(ctx context.Context, res *frontend.Result) (map[string]struct{}, error) {
	return computeEagerKeepSet(ctx, res), nil
}

// keepSet returns the current keep-set, or nil if none has been set yet.
// While nil, no filtering is applied (every ref is eligible).
func (ep *eagerPipeline) keepSet() map[string]struct{} {
	if p := ep.keepRefIDs.Load(); p != nil {
		return *p
	}
	return nil
}

// isKept reports whether refID should be retained. If no keep-set has been
// installed yet, every ref is considered kept.
func (ep *eagerPipeline) isKept(refID string) bool {
	keep := ep.keepSet()
	if keep == nil {
		return true
	}
	_, ok := keep[refID]
	return ok
}

func (ep *eagerPipeline) hasKeptRequester(refIDs map[string]struct{}) bool {
	keep := ep.keepSet()
	if keep == nil {
		return len(refIDs) > 0
	}
	for refID := range refIDs {
		if _, ok := keep[refID]; ok {
			return true
		}
	}
	return false
}

// trackerFor returns (creating if necessary) the pushTracker for digest.
func (ep *eagerPipeline) trackerFor(digest string) *pushTracker {
	if v, ok := ep.inflight.Load(digest); ok {
		return v.(*pushTracker)
	}
	nt := &pushTracker{refIDs: make(map[string]struct{})}
	actual, _ := ep.inflight.LoadOrStore(digest, nt)
	return actual.(*pushTracker)
}

// processRef compresses a single ref's blob (and its parent chain) and, in
// push mode, dispatches each pushable descriptor onto the push pool. Parent
// compression is deduplicated by flightcontrol inside computeBlobChain, so
// overlapping parent chains across workers are only compressed once.
func (ep *eagerPipeline) processRef(ref cache.ImmutableRef) error {
	ctx := ep.ctx
	s := session.NewGroup(ep.sessionID)
	refID := ref.ID()

	// If the keep-set is already installed (e.g. queued items processed
	// during shutdown drain), skip non-kept refs without paying for
	// compression.
	if !ep.isKept(refID) {
		bklog.G(ctx).Debugf("eager compress skipped (filtered) ref=%s", refID)
		return nil
	}

	bklog.G(ctx).Infof("eager compress starting ref=%s", refID)
	compressStart := time.Now()
	compressSpan, compressCtx := tracing.StartSpan(ctx, "eager compress ref", trace.WithAttributes(
		attribute.String("ref.id", refID),
	))
	rems, err := ref.GetRemotes(compressCtx, true, ep.refCfg, false, s)
	descCount, totalBytes := summarizeDescriptors(rems)
	compressSpan.SetAttributes(
		attribute.Int("descriptors", descCount),
		attribute.Int64("bytes", totalBytes),
	)
	tracing.FinishWithError(compressSpan, err)
	if err != nil {
		bklog.G(ctx).WithError(err).Warnf("eager compress failed ref=%s", refID)
		return err
	}
	bklog.G(ctx).Infof("eager compress done ref=%s descriptors=%d bytes=%d duration=%s",
		refID, descCount, totalBytes, time.Since(compressStart).Round(time.Millisecond))

	if ep.mode != EagerExportPush {
		return nil
	}

	// Re-check after the (potentially long) compress: the keep-set may
	// have been installed while we were inside GetRemotes.
	if !ep.isKept(refID) {
		bklog.G(ctx).Debugf("eager push skipped after compress (filtered) ref=%s", refID)
		return nil
	}

	pushStart := time.Now()
	if err := ep.dispatchPushes(ctx, refID, rems); err != nil {
		bklog.G(ctx).WithError(err).Warnf("eager push failed ref=%s", refID)
		return err
	}
	bklog.G(ctx).Infof("eager push dispatched ref=%s descriptors=%d bytes=%d duration=%s",
		refID, descCount, totalBytes, time.Since(pushStart).Round(time.Millisecond))
	return nil
}

func summarizeDescriptors(rems []*solver.Remote) (count int, totalBytes int64) {
	if len(rems) == 0 {
		return 0, 0
	}
	for _, desc := range rems[0].Descriptors {
		if !shouldEagerPushDesc(desc) {
			continue
		}
		count++
		totalBytes += desc.Size
	}
	return
}

// dispatchPushes sends every not-yet-enqueued pushable descriptor in a ref's
// chain to the push pool. It deliberately does not wait for completion; final
// export waits for the push pool after all compression workers have finished
// enqueueing.
func (ep *eagerPipeline) dispatchPushes(ctx context.Context, refID string, rems []*solver.Remote) error {
	if len(rems) == 0 {
		return nil
	}

	remote := rems[0]
	handler := retryhandler.New(
		limited.PushHandler(ep.pusher, remote.Provider, ep.pushCfg.TargetName),
		nil,
	)

	enqueued := 0
	for _, desc := range remote.Descriptors {
		if !shouldEagerPushDesc(desc) {
			continue
		}
		attempt, shouldEnqueue := ep.requestPushDescriptor(refID, desc)
		if !shouldEnqueue {
			continue
		}
		item := eagerPushItem{
			refID:   refID,
			desc:    desc,
			handler: handler,
			attempt: attempt,
		}
		select {
		case ep.pushWork <- item:
			enqueued++
		case <-ctx.Done():
			ep.unmarkQueuedDescriptor(refID, desc, attempt)
			return context.Cause(ctx)
		}
	}

	bklog.G(ctx).Debugf("eager push dispatched ref=%s enqueued=%d", refID, enqueued)
	return nil
}

func shouldEagerPushDesc(desc ocispecs.Descriptor) bool {
	return !images.IsNonDistributable(desc.MediaType)
}

// requestPushDescriptor records that refID needs desc and returns true only
// for the first requester that should enqueue actual push work. Duplicate
// requesters are bookkeeping only: they do not occupy push worker slots.
func (ep *eagerPipeline) requestPushDescriptor(refID string, desc ocispecs.Descriptor) (uint64, bool) {
	digest := desc.Digest.String()
	tracker := ep.trackerFor(digest)

	tracker.mu.Lock()
	defer tracker.mu.Unlock()

	tracker.refIDs[refID] = struct{}{}

	if tracker.pushed {
		bklog.G(ep.ctx).Debugf("eager push deduped ref=%s digest=%s size=%d", refID, digest, desc.Size)
		return 0, false
	}
	if !ep.hasKeptRequester(tracker.refIDs) {
		bklog.G(ep.ctx).Debugf("eager push skipped (filtered) ref=%s digest=%s size=%d", refID, digest, desc.Size)
		return 0, false
	}
	if tracker.enqueued || tracker.pushing {
		bklog.G(ep.ctx).Debugf("eager push already in progress ref=%s digest=%s size=%d", refID, digest, desc.Size)
		return 0, false
	}

	tracker.attempt++
	tracker.enqueued = true
	tracker.cancelled = false
	return tracker.attempt, true
}

func (ep *eagerPipeline) unmarkQueuedDescriptor(refID string, desc ocispecs.Descriptor, attempt uint64) {
	digest := desc.Digest.String()
	if v, ok := ep.inflight.Load(digest); ok {
		tracker := v.(*pushTracker)
		tracker.mu.Lock()
		if tracker.attempt == attempt && !tracker.pushing && !tracker.pushed {
			tracker.enqueued = false
		}
		delete(tracker.refIDs, refID)
		tracker.mu.Unlock()
	}
}

// pushDescriptor uploads a single descriptor. Deduplication has already
// happened at enqueue time, so push workers only handle real push attempts. If
// a kept requester arrives after a non-kept upload was cancelled, it can enqueue
// a fresh item once the cancelled attempt clears the tracker state.
func (ep *eagerPipeline) pushDescriptor(ctx context.Context, item eagerPushItem) error {
	digest := item.desc.Digest.String()
	parentCtx := ctx
	span, spanCtx := tracing.StartSpan(ctx, "eager push descriptor", trace.WithAttributes(
		attribute.String("ref.id", item.refID),
		attribute.String("digest", digest),
		attribute.Int64("size", item.desc.Size),
	))
	ctx = spanCtx
	tracker := ep.trackerFor(digest)

	tracker.mu.Lock()
	if item.attempt != 0 && tracker.attempt != item.attempt {
		tracker.mu.Unlock()
		span.SetAttributes(attribute.Bool("deduped", true))
		tracing.FinishWithError(span, nil)
		bklog.G(ctx).Debugf("eager push skipped stale attempt ref=%s digest=%s size=%d", item.refID, digest, item.desc.Size)
		return nil
	}
	tracker.refIDs[item.refID] = struct{}{}
	if tracker.pushed {
		tracker.enqueued = false
		tracker.mu.Unlock()
		span.SetAttributes(attribute.Bool("deduped", true))
		tracing.FinishWithError(span, nil)
		bklog.G(ctx).Debugf("eager push deduped ref=%s digest=%s size=%d", item.refID, digest, item.desc.Size)
		return nil
	}
	if !ep.hasKeptRequester(tracker.refIDs) {
		tracker.enqueued = false
		tracker.mu.Unlock()
		span.SetAttributes(attribute.Bool("filtered", true))
		tracing.FinishWithError(span, nil)
		bklog.G(ctx).Debugf("eager push skipped (filtered) ref=%s digest=%s size=%d", item.refID, digest, item.desc.Size)
		return nil
	}

	pushCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	tracker.enqueued = false
	tracker.pushing = true
	tracker.cancel = cancel
	tracker.cancelled = false
	tracker.mu.Unlock()
	defer func() {
		tracker.mu.Lock()
		if tracker.attempt == item.attempt {
			tracker.cancel = nil
			tracker.pushing = false
		}
		tracker.mu.Unlock()
	}()

	start := time.Now()
	bklog.G(ctx).Infof("eager push starting ref=%s digest=%s size=%d", item.refID, digest, item.desc.Size)

	_, err := item.handler(pushCtx, item.desc)
	if err == nil && pushCtx.Err() != nil && parentCtx.Err() == nil {
		tracker.mu.Lock()
		if tracker.attempt == item.attempt {
			tracker.pushing = false
			tracker.enqueued = false
			tracker.cancel = nil
			tracker.cancelled = true
		}
		tracker.mu.Unlock()
		span.SetAttributes(attribute.Bool("cancelled", true))
		tracing.FinishWithError(span, nil)
		bklog.G(ctx).Infof("eager push cancelled (filtered) ref=%s digest=%s size=%d after=%s",
			item.refID, digest, item.desc.Size, time.Since(start).Round(time.Millisecond))
		return nil
	}
	if err != nil {
		// Distinguish a deliberate keep-set cancellation from a real
		// failure: only the former has pushCtx done while the parent
		// ctx is still alive.
		if pushCtx.Err() != nil && parentCtx.Err() == nil {
			tracker.mu.Lock()
			if tracker.attempt == item.attempt {
				tracker.pushing = false
				tracker.enqueued = false
				tracker.cancel = nil
				tracker.cancelled = true
			}
			tracker.mu.Unlock()
			span.SetAttributes(attribute.Bool("cancelled", true))
			tracing.FinishWithError(span, nil)
			bklog.G(ctx).Infof("eager push cancelled (filtered) ref=%s digest=%s size=%d after=%s",
				item.refID, digest, item.desc.Size, time.Since(start).Round(time.Millisecond))
			return nil
		}
		tracing.FinishWithError(span, err)
		return err
	}

	tracker.mu.Lock()
	if tracker.attempt == item.attempt {
		tracker.pushed = true
		tracker.pushing = false
		tracker.enqueued = false
		tracker.cancel = nil
		tracker.cancelled = false
	}
	tracker.mu.Unlock()
	tracing.FinishWithError(span, nil)
	bklog.G(ctx).Infof("eager push done ref=%s digest=%s size=%d duration=%s",
		item.refID, digest, item.desc.Size, time.Since(start).Round(time.Millisecond))
	return nil
}

// wait shuts down both pools in order: stop new compress senders, drain
// compressWork, then drain pushWork. Compress workers enqueue push work without
// waiting for it; once compressWG.Wait returns, every reachable descriptor has
// either been enqueued or deduped, so closing pushWork is safe.
//
// The optional keep-set is the canonical set of ImmutableRef.ID()s that
// belong to the final image manifest, computed by the caller from the
// resolved frontend Result. When non-nil:
//   - any in-flight push whose every requester is non-kept is cancelled
//     immediately, freeing its registry-channel slot;
//   - any items still queued in pushWork or compressWork for non-kept refs
//     are skipped on dequeue;
//   - kept refs continue to compress and push as normal.
//
// Pass nil to keep every ref (back-compat for non-gateway frontends or
// callers that haven't computed a keep-set).
func (ep *eagerPipeline) wait(keepOpt ...map[string]struct{}) error {
	ep.waitOnce.Do(func() {
		var keep map[string]struct{}
		if len(keepOpt) > 0 {
			keep = keepOpt[0]
		}
		if keep != nil {
			cp := keep
			ep.keepRefIDs.Store(&cp)
			n := ep.cancelNonKeptInflight(keep)
			bklog.G(ep.ctx).Infof("eager wait keep_set_size=%d cancelled_inflight=%d", len(keep), n)
		}

		ep.closeMu.Lock()
		if ep.done == nil {
			ep.done = make(chan struct{})
		}
		ep.closing = true
		ep.closeMu.Unlock()

		close(ep.done)
		ep.senderWg.Wait()
		close(ep.compressWork)
		ep.compressWG.Wait()
		ep.drainCompress()

		if ep.pushWork != nil {
			close(ep.pushWork)
			ep.pushWG.Wait()
			ep.drainPushWork()
		}
	})
	ep.mu.Lock()
	defer ep.mu.Unlock()
	return ep.firstErr
}

// cancelNonKeptInflight walks every digest currently being pushed and
// cancels the upload if no requester is in the keep-set. Returns the
// number of digests for which at least one cancel was issued.
//
// A digest with at least one kept requester is left entirely alone. A digest
// whose requesters are all non-kept can have its single active push cancelled;
// queued items will observe the keep-set when dequeued and skip without pushing.
func (ep *eagerPipeline) cancelNonKeptInflight(keep map[string]struct{}) int {
	var digestsCancelled int
	ep.inflight.Range(func(key, val any) bool {
		digest := key.(string)
		tracker := val.(*pushTracker)

		tracker.mu.Lock()
		defer tracker.mu.Unlock()

		for refID := range tracker.refIDs {
			if _, ok := keep[refID]; ok {
				return true
			}
		}
		if tracker.cancel == nil {
			// Nothing in flight — queued items will be filtered by
			// isKept() at dequeue.
			return true
		}
		tracker.cancel()
		tracker.cancel = nil
		tracker.pushing = false
		tracker.enqueued = false
		tracker.cancelled = true
		tracker.attempt++
		digestsCancelled++
		bklog.G(ep.ctx).Infof("eager push cancel digest=%s requesters=%d cancelled_waiters=%d",
			digest, len(tracker.refIDs), 1)
		return true
	})
	return digestsCancelled
}

func (ep *eagerPipeline) applyExportRefs(exportRefs map[string]struct{}) int {
	cp := exportRefs
	ep.keepRefIDs.Store(&cp)
	return ep.cancelNonKeptInflight(exportRefs)
}

// drainCompress releases any refs left in compressWork after workers have
// exited (e.g. via ctx cancellation before the channel was drained).
func (ep *eagerPipeline) drainCompress() {
	for {
		select {
		case item, ok := <-ep.compressWork:
			if !ok {
				return
			}
			item.ref.Release(context.TODO())
		default:
			return
		}
	}
}

// drainPushWork clears any leftover queued push items after push workers exit.
func (ep *eagerPipeline) drainPushWork() {
	for {
		select {
		case item, ok := <-ep.pushWork:
			if !ok {
				return
			}
			ep.unmarkQueuedDescriptor(item.refID, item.desc, item.attempt)
		default:
			return
		}
	}
}
