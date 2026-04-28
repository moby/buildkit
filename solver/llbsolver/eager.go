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
	"github.com/moby/buildkit/util/flightcontrol"
	pushutil "github.com/moby/buildkit/util/push"
	"github.com/moby/buildkit/util/resolver/limited"
	"github.com/moby/buildkit/util/resolver/retryhandler"
	"github.com/moby/buildkit/worker"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
)

// defaultEagerWorkers and defaultEagerPushWorkers control the size of the
// compress and push pools respectively. They are deliberately set to 100 so
// that a single ref's full descriptor chain (typically 5-10 layers) can fan
// out into the push pool without serialization, matching what vanilla
// buildkit gets for free via images.Dispatch.
const (
	defaultEagerWorkers     = 100
	defaultEagerPushWorkers = 100
)

type eagerWorkItem struct {
	ref cache.ImmutableRef
}

// eagerPushItem is a single push descriptor handed to the push pool. The
// per-ref WaitGroup lets the dispatching compress worker block on completion
// of all its descriptor pushes; the per-ref errCh surfaces the first error
// from any of them.
type eagerPushItem struct {
	refID   string
	desc    ocispecs.Descriptor
	handler func(context.Context, ocispecs.Descriptor) ([]ocispecs.Descriptor, error)
	wg      *sync.WaitGroup
	errCh   chan<- error
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
	ctx context.Context

	// pusher is created at pipeline init when mode is EagerExportPush.
	pusher remotes.Pusher

	// pushDedup coalesces concurrent pushes of the same digest. After a push
	// completes, pushedDigests records the digest so subsequent calls
	// short-circuit immediately rather than re-running the flightcontrol
	// closure (which would emit redundant logs and consume registry
	// semaphore slots for no-op HEAD checks).
	pushDedup     flightcontrol.Group[struct{}]
	pushedDigests sync.Map

	// keepRefIDs is the set of ImmutableRef.ID()s whose blobs are part of
	// the final exported image manifest. It is populated once, by wait(),
	// after the frontend has fully resolved its result. While nil, every
	// ref's blobs are eligible for compression+push (the previous
	// behaviour). Once non-nil, processRef and pushDescriptor skip refs
	// whose ID is not in the set, and waitWithKeepSet cancels in-flight
	// pushes whose only requesters are non-kept refs.
	keepRefIDs atomic.Pointer[map[string]struct{}]
	// inflight tracks per-digest cancel funcs and the set of refIDs that
	// requested each digest. Used by waitWithKeepSet to cancel uploads
	// whose every requester turns out to be non-kept.
	inflight sync.Map // map[string]*pushTracker

	mu       sync.Mutex
	firstErr error
}

// pushTracker holds bookkeeping for a single digest's eager push:
//   - refIDs: every refID that asked for this digest (kept or not). Used
//     by cancelNonKeptInflight to decide whether any kept ref needs the
//     blob — if so, the closure stays alive.
//   - cancels: the cancel func of every in-flight pushDescriptor caller
//     (one per refID), keyed by refID. Cancellation must hit *every*
//     waiter because flightcontrol's closure-ctx is a sharedContext that
//     only fires done when *all* waiter ctxs are done (see
//     util/flightcontrol.sharedContext.checkDone). Cancelling just one
//     leaves the closure running.
type pushTracker struct {
	mu      sync.Mutex
	refIDs  map[string]struct{}
	cancels map[string]context.CancelFunc
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

	ep := &eagerPipeline{
		mode: mode,
		refCfg: cacheconfig.RefConfig{
			Compression: comp,
		},
		sessionID:    sessionID,
		pushCfg:      pushCfg,
		pusher:       pusher,
		ctx:          ctx,
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
		// Don't bail out on ctx.Done here: we need to drain pushWork so
		// the per-ref WaitGroup counters reach zero, otherwise compress
		// workers can deadlock in dispatchPushes during shutdown.
		item, ok := <-ep.pushWork
		if !ok {
			return
		}
		if err := ep.pushDescriptor(ep.ctx, item); err != nil {
			ep.recordErr(err)
			select {
			case item.errCh <- err:
			default:
			}
		}
		item.wg.Done()
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
	rems, err := ref.GetRemotes(ctx, true, ep.refCfg, false, s)
	if err != nil {
		bklog.G(ctx).WithError(err).Warnf("eager compress failed ref=%s", refID)
		return err
	}
	compressDur := time.Since(compressStart).Round(time.Millisecond)

	descCount, totalBytes := summarizeDescriptors(rems)
	bklog.G(ctx).Infof("eager compress done ref=%s descriptors=%d bytes=%d duration=%s",
		refID, descCount, totalBytes, compressDur)

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
	bklog.G(ctx).Infof("eager push complete ref=%s descriptors=%d bytes=%d duration=%s",
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

// dispatchPushes sends every pushable descriptor in a ref's chain to the
// push pool and waits for all of them to complete. Within a single ref this
// fans out to up to len(Descriptors) parallel uploads (gated by the push
// pool size and the registry's concurrency cap).
func (ep *eagerPipeline) dispatchPushes(ctx context.Context, refID string, rems []*solver.Remote) error {
	if len(rems) == 0 {
		return nil
	}

	remote := rems[0]
	handler := retryhandler.New(
		limited.PushHandler(ep.pusher, remote.Provider, ep.pushCfg.TargetName),
		nil,
	)

	var wg sync.WaitGroup
	errCh := make(chan error, len(remote.Descriptors))

	enqueued := 0
	for _, desc := range remote.Descriptors {
		if !shouldEagerPushDesc(desc) {
			continue
		}
		wg.Add(1)
		item := eagerPushItem{
			refID:   refID,
			desc:    desc,
			handler: handler,
			wg:      &wg,
			errCh:   errCh,
		}
		select {
		case ep.pushWork <- item:
			enqueued++
		case <-ctx.Done():
			wg.Done()
			waitWithCtx(ctx, &wg)
			return context.Cause(ctx)
		}
	}

	if err := waitWithCtx(ctx, &wg); err != nil {
		return err
	}

	close(errCh)
	for e := range errCh {
		if e != nil {
			return e
		}
	}
	return nil
}

// waitWithCtx blocks until wg reaches zero or ctx is cancelled. On
// cancellation it returns the ctx error without waiting (the WaitGroup
// counter may still be non-zero; it is safe to leak as the process is
// shutting down). On normal completion it returns nil.
func waitWithCtx(ctx context.Context, wg *sync.WaitGroup) error {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

func shouldEagerPushDesc(desc ocispecs.Descriptor) bool {
	return !images.IsNonDistributable(desc.MediaType)
}

// pushDescriptor uploads a single descriptor, deduplicated by digest. After
// the first successful push of a digest in this build, subsequent calls
// short-circuit at the pushedDigests check before entering the
// flightcontrol closure — so they emit no log, acquire no registry
// semaphore slot, and do no HEAD round trip.
//
// pushDescriptor also participates in the keep-set filter: it tracks every
// refID requesting each digest, lets cancelNonKeptInflight abort uploads
// whose requesters are all non-kept, and skips outright when the keep-set
// is already installed and item.refID is not in it.
//
// Kept refs that arrive *after* a cancellation pass simply re-enter
// flightcontrol with a fresh ctx: either the prior closure has already
// torn down (g.m[digest] is gone) and we start a new push, or the prior
// closure is still live and our kept ctx keeps it alive past
// cancelNonKeptInflight's reach.
func (ep *eagerPipeline) pushDescriptor(ctx context.Context, item eagerPushItem) error {
	digest := item.desc.Digest.String()
	if _, done := ep.pushedDigests.Load(digest); done {
		bklog.G(ctx).Debugf("eager push deduped ref=%s digest=%s size=%d", item.refID, digest, item.desc.Size)
		return nil
	}

	// Always record the requester first so cancelNonKeptInflight can
	// decide whether any kept ref needs this digest, even if we filter
	// below.
	tracker := ep.trackerFor(digest)
	tracker.mu.Lock()
	tracker.refIDs[item.refID] = struct{}{}
	tracker.mu.Unlock()

	if !ep.isKept(item.refID) {
		bklog.G(ctx).Debugf("eager push skipped (filtered) ref=%s digest=%s size=%d", item.refID, digest, item.desc.Size)
		return nil
	}

	pushCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	tracker.mu.Lock()
	if tracker.cancels == nil {
		tracker.cancels = make(map[string]context.CancelFunc)
	}
	tracker.cancels[item.refID] = cancel
	tracker.mu.Unlock()
	defer func() {
		tracker.mu.Lock()
		delete(tracker.cancels, item.refID)
		tracker.mu.Unlock()
	}()

	start := time.Now()
	bklog.G(ctx).Infof("eager push starting ref=%s digest=%s size=%d", item.refID, digest, item.desc.Size)

	_, err := ep.pushDedup.Do(pushCtx, digest, func(ctx context.Context) (struct{}, error) {
		_, herr := item.handler(ctx, item.desc)
		return struct{}{}, herr
	})
	if err != nil {
		// Distinguish a deliberate keep-set cancellation from a real
		// failure: only the former has pushCtx done while the parent
		// ctx is still alive.
		if pushCtx.Err() != nil && ctx.Err() == nil {
			bklog.G(ctx).Infof("eager push cancelled (filtered) ref=%s digest=%s size=%d after=%s",
				item.refID, digest, item.desc.Size, time.Since(start).Round(time.Millisecond))
			return nil
		}
		return err
	}

	ep.pushedDigests.Store(digest, struct{}{})
	bklog.G(ctx).Infof("eager push done ref=%s digest=%s size=%d duration=%s",
		item.refID, digest, item.desc.Size, time.Since(start).Round(time.Millisecond))
	return nil
}

// wait shuts down both pools in order: stop new compress senders, drain
// compressWork, then drain pushWork. Compress workers are guaranteed to
// have all dispatched their pushes by the time compressWG.Wait returns
// (because dispatchPushes blocks on the per-ref WaitGroup), so closing
// pushWork at that point is safe.
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
func (ep *eagerPipeline) wait(keep map[string]struct{}) error {
	ep.waitOnce.Do(func() {
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
// CRITICAL: flightcontrol's closure ctx is a sharedContext whose Done()
// only fires once *every* registered waiter ctx is done (see
// util/flightcontrol.sharedContext.checkDone). Cancelling a single
// waiter is not enough — the closure keeps running, the upload finishes,
// and we get a ~6 GB freebie we never wanted. So we cancel every waiter
// for the digest in one pass.
//
// A digest with at least one kept requester is left entirely alone: that
// kept waiter's pushDescriptor still needs the closure's result, and
// flightcontrol will deliver it.
//
// Cancelled uploads return from flightcontrol.Do with a ctx error;
// pushDescriptor recognises that as an intentional skip (pushCtx.Err
// non-nil while the parent ctx is alive) and converts it to a nil error
// so the build doesn't fail.
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
		if len(tracker.cancels) == 0 {
			// Nothing in flight — queued items will be filtered by
			// isKept() at dequeue.
			return true
		}
		n := len(tracker.cancels)
		for refID, cancelFn := range tracker.cancels {
			cancelFn()
			delete(tracker.cancels, refID)
		}
		digestsCancelled++
		bklog.G(ep.ctx).Infof("eager push cancel digest=%s requesters=%d cancelled_waiters=%d",
			digest, len(tracker.refIDs), n)
		return true
	})
	return digestsCancelled
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

// drainPushWork marks any leftover push items' WaitGroups as Done so that
// dispatching compress workers (if any are still blocked) can unblock.
func (ep *eagerPipeline) drainPushWork() {
	for {
		select {
		case item, ok := <-ep.pushWork:
			if !ok {
				return
			}
			item.wg.Done()
		default:
			return
		}
	}
}
