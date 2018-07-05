package forwarder

import (
	"context"
	"sync"

	"github.com/moby/buildkit/cache"
	clienttypes "github.com/moby/buildkit/client"
	"github.com/moby/buildkit/frontend"
	"github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/solver"
	"github.com/moby/buildkit/util/apicaps"
	"github.com/moby/buildkit/worker"
	"github.com/pkg/errors"
)

func llbBridgeToGatewayClient(ctx context.Context, llbBridge frontend.FrontendLLBBridge, opts map[string]string, workerInfos []clienttypes.WorkerInfo) (*bridgeClient, error) {
	return &bridgeClient{
		opts:              opts,
		FrontendLLBBridge: llbBridge,
		sid:               session.FromContext(ctx),
		workerInfos:       workerInfos,
		final:             map[*ref]struct{}{},
	}, nil
}

type bridgeClient struct {
	frontend.FrontendLLBBridge
	mu           sync.Mutex
	opts         map[string]string
	final        map[*ref]struct{}
	sid          string
	exporterAttr map[string][]byte
	refs         []*ref
	workerInfos  []clienttypes.WorkerInfo
}

func (c *bridgeClient) Solve(ctx context.Context, req client.SolveRequest) (*client.Result, error) {
	refs, exporterAttrRes, err := c.FrontendLLBBridge.Solve(ctx, frontend.SolveRequest{
		Definition:      req.Definition,
		Frontend:        req.Frontend,
		FrontendOpt:     req.FrontendOpt,
		ImportCacheRefs: req.ImportCacheRefs,
	})
	if err != nil {
		return nil, err
	}

	res := &client.Result{}
	c.mu.Lock()
	for k, r := range refs {
		rr := &ref{r}
		c.refs = append(c.refs, rr)
		res.AddRef(k, rr)
	}
	c.mu.Unlock()
	res.Metadata = exporterAttrRes

	return res, nil
}
func (c *bridgeClient) BuildOpts() client.BuildOpts {
	workers := make([]client.WorkerInfo, 0, len(c.workerInfos))
	for _, w := range c.workerInfos {
		workers = append(workers, client.WorkerInfo(w))
	}

	return client.BuildOpts{
		Opts:      c.opts,
		SessionID: c.sid,
		Workers:   workers,
		Product:   apicaps.ExportedProduct,
	}
}

func (c *bridgeClient) result(r *client.Result) (map[string]solver.CachedResult, map[string][]byte, error) {
	refs := map[string]solver.CachedResult{}
	for k, r := range r.Refs {
		rr, ok := r.(*ref)
		if !ok {
			return nil, nil, errors.Errorf("invalid reference type for forward %T", r)
		}
		c.final[rr] = struct{}{}
		refs[k] = rr.CachedResult
	}
	return refs, r.Metadata, nil
}

func (c *bridgeClient) discard(err error) {
	for _, r := range c.refs {
		if r != nil {
			if _, ok := c.final[r]; !ok || err != nil {
				r.Release(context.TODO())
			}
		}
	}
}

type ref struct {
	solver.CachedResult
}

func (r *ref) ReadFile(ctx context.Context, req client.ReadRequest) ([]byte, error) {
	ref, err := r.getImmutableRef()
	if err != nil {
		return nil, err
	}
	newReq := cache.ReadRequest{
		Filename: req.Filename,
	}
	if r := req.Range; r != nil {
		newReq.Range = &cache.FileRange{
			Offset: r.Offset,
			Length: r.Length,
		}
	}
	return cache.ReadFile(ctx, ref, newReq)
}

func (r *ref) getImmutableRef() (cache.ImmutableRef, error) {
	ref, ok := r.CachedResult.Sys().(*worker.WorkerRef)
	if !ok {
		return nil, errors.Errorf("invalid ref: %T", r.CachedResult.Sys())
	}
	return ref.ImmutableRef, nil
}
