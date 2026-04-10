package llbsolver

import (
	"context"
	"os"
	"runtime"
	"strconv"
	"sync"

	"github.com/containerd/containerd/v2/core/remotes"
	"github.com/moby/buildkit/cache"
	cacheconfig "github.com/moby/buildkit/cache/config"
	"github.com/moby/buildkit/exporter"
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
	pushCfg   *exporter.EagerPushConfig

	work chan eagerWorkItem
	wg   sync.WaitGroup

	// ctx carries the lease so compressed blobs are GC-protected.
	ctx context.Context

	// pusher is created at pipeline init when mode is EagerExportPush.
	pusher remotes.Pusher

	// pushDedup prevents concurrent pushes of the same digest.
	pushDedup flightcontrol.Group[struct{}]

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
			SkipParents: true,
		},
		sessionID: sessionID,
		pushCfg:   pushCfg,
		pusher:    pusher,
		ctx:       ctx,
		work:      make(chan eagerWorkItem, 256),
	}

	numWorkers := eagerWorkerCount()
	ep.wg.Add(numWorkers)
	for range numWorkers {
		go ep.worker()
	}

	return ep, nil
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

// processRef compresses a single ref's blob and optionally pushes it.
// SkipParents is set in the refCfg, so only this ref's own layer is
// compressed — parent layers get their own callbacks and worker items.
func (ep *eagerPipeline) processRef(ref cache.ImmutableRef) error {
	ctx := ep.ctx
	s := session.NewGroup(ep.sessionID)

	bklog.G(ctx).Debugf("eager compress starting for ref %s", ref.ID())
	remotes, err := ref.GetRemotes(ctx, true, ep.refCfg, false, s)
	if err != nil {
		bklog.G(ctx).WithError(err).Warnf("eager compress failed for ref %s", ref.ID())
		return err
	}
	bklog.G(ctx).Debugf("eager compress done for ref %s", ref.ID())

	if ep.mode == EagerExportPush {
		if err := ep.pushBlobs(ctx, remotes); err != nil {
			bklog.G(ctx).WithError(err).Warnf("eager push failed for ref %s", ref.ID())
			return err
		}
		bklog.G(ctx).Debugf("eager push done for ref %s", ref.ID())
	}
	return nil
}

// pushBlobs pushes each layer descriptor from the GetRemotes result to the
// registry. Pushes are deduplicated by digest via flightcontrol — if two
// workers try to push the same blob concurrently, only one upload happens.
func (ep *eagerPipeline) pushBlobs(ctx context.Context, rems []*solver.Remote) error {
	if len(rems) == 0 {
		return nil
	}

	remote := rems[0]
	handler := retryhandler.New(
		limited.PushHandler(ep.pusher, remote.Provider, ep.pushCfg.TargetName),
		nil,
	)

	for _, desc := range remote.Descriptors {
		if err := ep.pushBlob(ctx, handler, desc); err != nil {
			return err
		}
	}
	return nil
}

// pushBlob pushes a single descriptor, deduplicated by digest across all
// concurrent workers via flightcontrol.
func (ep *eagerPipeline) pushBlob(ctx context.Context, handler func(context.Context, ocispecs.Descriptor) ([]ocispecs.Descriptor, error), desc ocispecs.Descriptor) error {
	_, err := ep.pushDedup.Do(ctx, desc.Digest.String(), func(ctx context.Context) (struct{}, error) {
		bklog.G(ctx).Debugf("eager pushing blob %s (%d bytes)", desc.Digest, desc.Size)
		_, err := handler(ctx, desc)
		return struct{}{}, err
	})
	return err
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
