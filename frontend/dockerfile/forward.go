package dockerfile

import (
	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/frontend"
	"github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/solver/pb"
	"golang.org/x/net/context"
)

func llbBridgeToGatewayClient(ctx context.Context, llbBridge frontend.FrontendLLBBridge, opts map[string]string) (*bridgeClient, error) {
	return &bridgeClient{opts: opts, FrontendLLBBridge: llbBridge, sid: session.FromContext(ctx)}, nil
}

type bridgeClient struct {
	frontend.FrontendLLBBridge
	opts         map[string]string
	final        cache.ImmutableRef
	sid          string
	exporterAttr map[string][]byte
	refs         []*ref
}

func (c *bridgeClient) Solve(ctx context.Context, def *pb.Definition, f string, exporterAttr map[string][]byte, final bool) (client.Reference, error) {
	r, exporterAttrRes, err := c.FrontendLLBBridge.Solve(ctx, frontend.SolveRequest{
		Definition: def,
		Frontend:   f,
	})
	if err != nil {
		return nil, err
	}
	rr := &ref{r}
	c.refs = append(c.refs, rr)
	if final {
		c.final = rr
		if exporterAttr == nil {
			exporterAttr = make(map[string][]byte)
		}
		for k, v := range exporterAttrRes {
			exporterAttr[k] = v
		}
		c.exporterAttr = exporterAttr
	}
	return rr, nil
}
func (c *bridgeClient) Opts() map[string]string {
	return c.opts
}
func (c *bridgeClient) SessionID() string {
	return c.sid
}

type ref struct {
	cache.ImmutableRef
}

func (r *ref) ReadFile(ctx context.Context, fp string) ([]byte, error) {
	return cache.ReadFile(ctx, r.ImmutableRef, fp)
}
