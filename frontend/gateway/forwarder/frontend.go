package forwarder

import (
	"context"

	"github.com/moby/buildkit/frontend"
	"github.com/moby/buildkit/frontend/dockerfile/builder"
	"github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/solver"
)

func NewGatewayForwarder(w frontend.WorkerInfos, f client.BuildFunc) frontend.Frontend {
	return &GatewayForwarder{
		workers: w,
		f:       f,
	}
}

type GatewayForwarder struct {
	workers frontend.WorkerInfos
	f       client.BuildFunc
}

func (gf *GatewayForwarder) Solve(ctx context.Context, llbBridge frontend.FrontendLLBBridge, opts map[string]string) (retRef solver.CachedResult, exporterAttr map[string][]byte, retErr error) {
	c, err := llbBridgeToGatewayClient(ctx, llbBridge, opts, gf.workers.WorkerInfos())
	if err != nil {
		return nil, nil, err
	}

	defer func() {
		for _, r := range c.refs {
			if r != nil && (c.final != r || retErr != nil) {
				r.Release(context.TODO())
			}
		}
	}()

	if err := builder.Build(ctx, c); err != nil {
		return nil, nil, err
	}

	if c.final == nil || c.final.CachedResult == nil {
		return nil, c.exporterAttr, nil
	}

	return c.final, c.exporterAttr, nil
}
