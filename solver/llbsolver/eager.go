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

// Keep the pools large enough for a ref's descriptor chain to fan out without
// serializing pushes.
const (
	defaultEagerWorkers     = 100
	defaultEagerPushWorkers = 100
)

type eagerWorkItem struct {
	ref cache.ImmutableRef
}

// eagerPushItem is one descriptor handed to the push pool.
type eagerPushItem struct {
	refID   string
	desc    ocispecs.Descriptor
	handler func(context.Context, ocispecs.Descriptor) ([]ocispecs.Descriptor, error)
	wg      *sync.WaitGroup
	errCh   chan<- error
}

// eagerPipeline compresses refs as vertices complete and optionally pushes
// their descriptors through a separate pool for per-chain parallelism.
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

	// closeMu stops new senders from entering shutdown races.
	closeMu  sync.Mutex
	closing  bool
	senderWg sync.WaitGroup
	waitOnce sync.Once

	// ctx carries the lease that keeps compressed blobs GC-protected.
	ctx context.Context

	pusher remotes.Pusher

	// pushDedup coalesces concurrent pushes; pushedDigests skips digests that
	// already succeeded in this build.
	pushDedup     flightcontrol.Group[struct{}]
	pushedDigests sync.Map

	// keepRefIDs is nil until wait() installs the final image ref set. After
	// that, non-kept refs are skipped or cancelled.
	keepRefIDs atomic.Pointer[map[string]struct{}]
	// inflight tracks requesters and cancel funcs per digest.
	inflight sync.Map // map[string]*pushTracker

	mu       sync.Mutex
	firstErr error
}

// pushTracker records every ref that requested a digest and every active
// waiter. flightcontrol only cancels the shared push when all waiters cancel.
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
		// Drain pushWork even after ctx cancellation so per-ref waiters finish.
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

// onVertexComplete clones finished refs and enqueues them for eager work.
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

// computeEagerKeepSet returns every ref ID that contributes to the final image,
// including parent-chain layers. Each layer has its own vertex/ref ID, so
// keeping only the final ref would filter out the eager layer pushes.
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
		// LayerChain returns clones, so release them after reading IDs.
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

// keepSet returns nil until filtering is enabled.
func (ep *eagerPipeline) keepSet() map[string]struct{} {
	if p := ep.keepRefIDs.Load(); p != nil {
		return *p
	}
	return nil
}

// isKept treats every ref as kept until a keep-set is installed.
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

// processRef compresses a ref's chain and, in push mode, dispatches pushable
// descriptors. Parent-chain compression is deduplicated lower in the cache.
func (ep *eagerPipeline) processRef(ref cache.ImmutableRef) error {
	ctx := ep.ctx
	s := session.NewGroup(ep.sessionID)
	refID := ref.ID()

	// Skip queued work that became irrelevant before compression started.
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

	// The keep-set may have been installed during GetRemotes.
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

// dispatchPushes fans a ref's pushable descriptors out to the push pool.
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

// waitWithCtx returns early on context cancellation instead of blocking shutdown.
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

// pushDescriptor deduplicates by digest and records requesters so wait() can
// cancel pushes that only serve non-final refs.
func (ep *eagerPipeline) pushDescriptor(ctx context.Context, item eagerPushItem) error {
	digest := item.desc.Digest.String()
	if _, done := ep.pushedDigests.Load(digest); done {
		bklog.G(ctx).Debugf("eager push deduped ref=%s digest=%s size=%d", item.refID, digest, item.desc.Size)
		return nil
	}

	// Record before filtering so cancellation can see all requesters.
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
		// A cancelled pushCtx with a live parent means keep-set filtering won.
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

// wait installs the optional keep-set, cancels in-flight pushes that only serve
// non-kept refs, then drains the compress and push pools in order.
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

// cancelNonKeptInflight cancels digests whose requesters are all non-kept.
// All waiters for a digest must be cancelled, or flightcontrol keeps the
// shared push running.
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
			// Queued items will be filtered at dequeue.
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

// drainCompress releases refs left behind after workers exit.
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

// drainPushWork unblocks any dispatchers still waiting on queued push items.
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
