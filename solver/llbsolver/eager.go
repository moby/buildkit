package llbsolver

import (
	"context"
	"os"
	"runtime"
	"strconv"
	"sync"

	"github.com/moby/buildkit/cache"
	cacheconfig "github.com/moby/buildkit/cache/config"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/solver"
	"github.com/moby/buildkit/util/bklog"
	"github.com/moby/buildkit/util/compression"
	"github.com/moby/buildkit/worker"
)

const defaultEagerWorkers = 4

type eagerWorkItem struct {
	ref cache.ImmutableRef
}

// eagerPipeline manages background compression (and optionally push) of layer
// blobs as build vertices complete, rather than deferring all work to finalize.
type eagerPipeline struct {
	mode      EagerExportMode
	refCfg    cacheconfig.RefConfig
	sessionID string
	sm        *session.Manager

	work chan eagerWorkItem
	wg   sync.WaitGroup

	// ctx carries the lease so compressed blobs are GC-protected.
	ctx context.Context

	mu       sync.Mutex
	firstErr error
}

func eagerWorkerCount() int {
	if s := os.Getenv("BUILDKIT_EAGER_EXPORT_WORKERS"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			return n
		}
	}
	return max(defaultEagerWorkers, runtime.NumCPU())
}

func newEagerPipeline(ctx context.Context, mode EagerExportMode, comp compression.Config, sessionID string, sm *session.Manager) *eagerPipeline {
	ep := &eagerPipeline{
		mode: mode,
		refCfg: cacheconfig.RefConfig{
			Compression: comp,
			SkipParents: true,
		},
		sessionID: sessionID,
		sm:        sm,
		ctx:       ctx,
		work:      make(chan eagerWorkItem, 256),
	}

	numWorkers := eagerWorkerCount()
	ep.wg.Add(numWorkers)
	for range numWorkers {
		go ep.worker()
	}

	return ep
}

func (ep *eagerPipeline) worker() {
	defer ep.wg.Done()
	for {
		select {
		case <-ep.ctx.Done():
			return
		case item, ok := <-ep.work:
			if !ok {
				return
			}
			if err := ep.processRef(item.ref); err != nil {
				ep.mu.Lock()
				if ep.firstErr == nil {
					ep.firstErr = err
				}
				ep.mu.Unlock()
			}
			item.ref.Release(context.TODO())
		}
	}
}

// onVertexComplete is the callback registered on the solver Job. It extracts
// ImmutableRefs from vertex results, clones them for safe async use, and
// sends them to the worker pool for compression.
func (ep *eagerPipeline) onVertexComplete(vtx solver.Vertex, results []solver.Result) {
	for _, res := range results {
		if res == nil {
			continue
		}
		workerRef, ok := res.Sys().(*worker.WorkerRef)
		if !ok || workerRef.ImmutableRef == nil {
			continue
		}

		cloned := workerRef.ImmutableRef.Clone()
		select {
		case ep.work <- eagerWorkItem{ref: cloned}:
		case <-ep.ctx.Done():
			cloned.Release(context.TODO())
			return
		}
	}
}

// processRef compresses a single ref's blob. SkipParents is set in the refCfg,
// so only this ref's own layer is compressed — parent layers get their own
// callbacks and worker items.
func (ep *eagerPipeline) processRef(ref cache.ImmutableRef) error {
	ctx := ep.ctx
	s := session.NewGroup(ep.sessionID)

	bklog.G(ctx).Debugf("eager compress starting for ref %s", ref.ID())
	_, err := ref.GetRemotes(ctx, true, ep.refCfg, false, s)
	if err != nil {
		bklog.G(ctx).WithError(err).Warnf("eager compress failed for ref %s", ref.ID())
		return err
	}
	bklog.G(ctx).Debugf("eager compress done for ref %s", ref.ID())

	if ep.mode == EagerExportPush {
		// TODO: implement eager blob push
		bklog.G(ctx).Debugf("eager push not yet implemented for ref %s", ref.ID())
	}
	return nil
}

// wait closes the work channel and blocks until all workers finish.
// Returns the first error encountered by any worker.
func (ep *eagerPipeline) wait() error {
	close(ep.work)
	ep.wg.Wait()
	// Release any refs left in the channel (e.g. if workers exited early
	// due to context cancellation).
	for item := range ep.work {
		item.ref.Release(context.TODO())
	}
	ep.mu.Lock()
	defer ep.mu.Unlock()
	return ep.firstErr
}
