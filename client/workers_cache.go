package client

import (
	"context"
	"maps"
	"slices"
	"sync"

	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/stats"
)

// workerCache stores one unfiltered worker list for the active connection.
// A reconnect drops it. Filtered queries never read or write this cache.
type workerCache struct {
	mu sync.Mutex

	workers []*WorkerInfo
	valid   bool

	// up reports whether a connection is currently established.
	up bool
	// ends counts connection terminations. A fetch is cached only when no
	// connection ended while it was in flight.
	ends uint64
}

var _ stats.Handler = (*workerCache)(nil)

func (c *Client) connStatsHandler() stats.Handler {
	return &c.workerCache
}

func (w *workerCache) TagRPC(ctx context.Context, _ *stats.RPCTagInfo) context.Context {
	return ctx
}

func (w *workerCache) HandleRPC(context.Context, stats.RPCStats) {}

func (w *workerCache) TagConn(ctx context.Context, _ *stats.ConnTagInfo) context.Context {
	return ctx
}

func (w *workerCache) HandleConn(_ context.Context, cs stats.ConnStats) {
	if !cs.IsClient() {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	switch cs.(type) {
	case *stats.ConnBegin:
		w.up = true
		// The new connection has not been listed yet.
		w.valid = false
		w.workers = nil
	case *stats.ConnEnd:
		w.up = false
		w.valid = false
		w.workers = nil
		w.ends++
	}
}

func (w *workerCache) load(conn *grpc.ClientConn) ([]*WorkerInfo, bool) {
	if conn == nil {
		return nil, false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.valid || !w.up || conn.GetState() != connectivity.Ready {
		return nil, false
	}
	return cloneWorkerInfos(w.workers), true
}

func (w *workerCache) endsSnapshot() uint64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.ends
}

func (w *workerCache) store(conn *grpc.ClientConn, workers []*WorkerInfo, endsBefore uint64) {
	if conn == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.up || w.ends != endsBefore || conn.GetState() != connectivity.Ready {
		return
	}
	w.workers = cloneWorkerInfos(workers)
	w.valid = true
}

func cloneWorkerInfos(in []*WorkerInfo) []*WorkerInfo {
	if in == nil {
		return nil
	}
	out := make([]*WorkerInfo, len(in))
	for i, w := range in {
		if w == nil {
			continue
		}
		cp := *w
		cp.Labels = maps.Clone(w.Labels)
		cp.Platforms = clonePlatforms(w.Platforms)
		cp.GCPolicy = clonePruneInfos(w.GCPolicy)
		cp.CDIDevices = cloneCDIDevices(w.CDIDevices)
		out[i] = &cp
	}
	return out
}

func clonePlatforms(in []ocispecs.Platform) []ocispecs.Platform {
	if in == nil {
		return nil
	}
	out := make([]ocispecs.Platform, len(in))
	for i, p := range in {
		p.OSFeatures = slices.Clone(p.OSFeatures)
		out[i] = p
	}
	return out
}

func clonePruneInfos(in []PruneInfo) []PruneInfo {
	if in == nil {
		return nil
	}
	out := make([]PruneInfo, len(in))
	for i, p := range in {
		p.Filter = slices.Clone(p.Filter)
		out[i] = p
	}
	return out
}

func cloneCDIDevices(in []CDIDevice) []CDIDevice {
	if in == nil {
		return nil
	}
	out := make([]CDIDevice, len(in))
	for i, d := range in {
		d.Annotations = maps.Clone(d.Annotations)
		out[i] = d
	}
	return out
}
