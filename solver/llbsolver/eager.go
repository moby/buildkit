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
	result  chan<- error
}

// eagerPipeline receives completed vertex refs, compresses their layer chains,
// and optionally fans out pushable descriptors to a separate push pool. Once
// the final export refs are known, it filters/cancels non-export work and
// drains both pools.
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
	ctx    context.Context
	cancel context.CancelCauseFunc

	pusher remotes.Pusher

	// pushDedup coalesces concurrent pushes; pushedDigests skips digests that
	// already succeeded in this build.
	pushDedup     flightcontrol.Group[bool]
	pushedDigests sync.Map

	// exportRefIDs is nil until applyExportRefs installs the final export refs.
	// After that, non-export refs are skipped or cancelled.
	exportRefIDs atomic.Pointer[map[string]struct{}]
	// inflight tracks requesters and cancel funcs per digest.
	inflight sync.Map // map[string]*pushTracker

	mu       sync.Mutex
	firstErr error
}

// pushTracker is per digest. refIDs records every ref that requested the blob,
// including refs later filtered out of the final export; cancels records only
// active push waiters. When the final export refs are known, a digest is
// cancelled only if none of its requesters are part of the export. If it is
// cancelled, every active waiter must be cancelled because flightcontrol keeps
// the shared push running until all waiter contexts are done.
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
		cancel:       cancel,
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
		if err := ep.pushDescriptor(item); err != nil {
			ep.recordErr(err)
			item.result <- err
		} else {
			item.result <- nil
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

// computeEagerExportRefs returns every ref ID that contributes to the final image,
// including parent-chain layers. Each layer has its own vertex/ref ID, so
// exporting only the final ref would filter out the eager layer pushes.
func computeEagerExportRefs(ctx context.Context, res *frontend.Result) map[string]struct{} {
	exportRefs := make(map[string]struct{})
	if res == nil {
		return exportRefs
	}
	res.EachRef(func(rp solver.ResultProxy) error {
		if rp == nil {
			return nil
		}
		cached, err := rp.Result(ctx)
		if err != nil {
			bklog.G(ctx).WithError(err).Warnf("eager export ref set: failed to resolve ref")
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
			exportRefs[layer.ID()] = struct{}{}
		}
		if err := chain.Release(context.WithoutCancel(ctx)); err != nil {
			bklog.G(ctx).WithError(err).Warnf("eager export ref set: failed to release chain clones")
		}
		return nil
	})
	return exportRefs
}

// isExportRef treats every ref as exportable until an export ref set is installed.
func (ep *eagerPipeline) isExportRef(refID string) bool {
	exportRefs := ep.exportRefIDs.Load()
	if exportRefs == nil {
		return true
	}
	_, ok := (*exportRefs)[refID]
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
	if !ep.isExportRef(refID) {
		bklog.G(ctx).Debugf("eager compress skipped (filtered) ref=%s", refID)
		return nil
	}

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

	if ep.mode != EagerExportPush {
		return nil
	}

	// The export ref set may have been installed during GetRemotes.
	if !ep.isExportRef(refID) {
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
	// Buffered to the descriptor count so workers can report completion even if
	// dispatchPushes returns early on context cancellation.
	resultCh := make(chan error, len(remote.Descriptors))
	handler := retryhandler.New(
		limited.PushHandler(ep.pusher, remote.Provider, ep.pushCfg.TargetName),
		nil,
	)

	enqueued := 0
	for _, desc := range remote.Descriptors {
		if !shouldEagerPushDesc(desc) {
			continue
		}
		item := eagerPushItem{
			refID:   refID,
			desc:    desc,
			handler: handler,
			result:  resultCh,
		}
		select {
		case ep.pushWork <- item:
			enqueued++
		case <-ctx.Done():
			return context.Cause(ctx)
		}
	}

	for range enqueued {
		select {
		case err := <-resultCh:
			if err != nil {
				return err
			}
		case <-ctx.Done():
			return context.Cause(ctx)
		}
	}
	return nil
}

func shouldEagerPushDesc(desc ocispecs.Descriptor) bool {
	return !images.IsNonDistributable(desc.MediaType)
}

// pushDescriptor deduplicates by digest and records requesters so wait() can
// cancel pushes that only serve non-final refs.
func (ep *eagerPipeline) pushDescriptor(item eagerPushItem) error {
	ctx := ep.ctx
	digest := item.desc.Digest.String()
	span, pushCtx := tracing.StartSpan(ctx, "eager push descriptor", trace.WithAttributes(
		attribute.String("ref.id", item.refID),
		attribute.String("digest", digest),
		attribute.Int64("size", item.desc.Size),
	))
	if _, done := ep.pushedDigests.Load(digest); done {
		span.SetAttributes(attribute.Bool("deduped", true))
		span.End()
		return nil
	}

	tracker := ep.trackerFor(digest)
	pushCtx, cancel := context.WithCancelCause(pushCtx)
	defer cancel(errors.WithStack(context.Canceled))

	// Register requester and cancel func atomically with export-ref filtering
	// so cancelNonExportInflight cannot miss an about-to-start push.
	tracker.mu.Lock()
	tracker.refIDs[item.refID] = struct{}{}
	if !ep.isExportRef(item.refID) {
		tracker.mu.Unlock()
		span.SetAttributes(attribute.Bool("filtered", true))
		span.End()
		return nil
	}
	if tracker.cancels == nil {
		tracker.cancels = make(map[string]context.CancelFunc)
	}
	tracker.cancels[item.refID] = func() { cancel(errors.WithStack(context.Canceled)) }
	tracker.mu.Unlock()

	defer func() {
		tracker.mu.Lock()
		delete(tracker.cancels, item.refID)
		tracker.mu.Unlock()
	}()

	pushed, err := ep.pushDedup.Do(pushCtx, digest, func(ctx context.Context) (bool, error) {
		if _, done := ep.pushedDigests.Load(digest); done {
			return false, nil
		}
		_, herr := item.handler(ctx, item.desc)
		if herr != nil {
			return false, herr
		}
		ep.pushedDigests.Store(digest, struct{}{})
		return true, nil
	})
	if err != nil {
		// A cancelled pushCtx with a live parent means export ref set filtering won.
		if context.Cause(pushCtx) != nil && context.Cause(ctx) == nil {
			span.SetAttributes(attribute.Bool("cancelled", true))
			span.End()
			return nil
		}
		tracing.FinishWithError(span, err)
		return err
	}
	if !pushed {
		span.SetAttributes(attribute.Bool("deduped", true))
		span.End()
		return nil
	}

	span.End()
	return nil
}

// applyExportRefs installs the final export refs and cancels in-flight pushes that
// only serve non-export refs.
func (ep *eagerPipeline) applyExportRefs(exportRefs map[string]struct{}) int {
	if exportRefs == nil {
		return 0
	}
	cp := exportRefs
	ep.exportRefIDs.Store(&cp)
	return ep.cancelNonExportInflight(exportRefs)
}

// wait drains the compress and push pools in order.
func (ep *eagerPipeline) wait() error {
	ep.waitOnce.Do(func() {
		if ep.cancel != nil {
			defer ep.cancel(errors.WithStack(context.Canceled))
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

// cancelNonExportInflight cancels digests whose requesters are all non-export.
// All waiters for a digest must be cancelled, or flightcontrol leaves the
// shared push running.
func (ep *eagerPipeline) cancelNonExportInflight(exportRefs map[string]struct{}) int {
	var digestsCancelled int
	ep.inflight.Range(func(key, val any) bool {
		digest := key.(string)
		tracker := val.(*pushTracker)

		tracker.mu.Lock()
		defer tracker.mu.Unlock()

		for refID := range tracker.refIDs {
			if _, ok := exportRefs[refID]; ok {
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
			item.result <- nil
		default:
			return
		}
	}
}
