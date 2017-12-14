package dockerfile

import (
	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/frontend"
	"github.com/moby/buildkit/frontend/dockerfile/builder"
	"github.com/moby/buildkit/util/appcontext"
	"github.com/pkg/errors"
	"golang.org/x/net/context"
)

func NewDockerfileFrontend() frontend.Frontend {
	return &dfFrontend{}
}

type dfFrontend struct{}

func (f *dfFrontend) Solve(ctx context.Context, llbBridge frontend.FrontendLLBBridge, opts map[string]string) (retRef cache.ImmutableRef, exporterAttr map[string][]byte, retErr error) {

	c, err := llbBridgeToGatewayClient(ctx, llbBridge, opts)
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

	if err := builder.Build(appcontext.Context(), c); err != nil {
		return nil, nil, err
	}

	if c.final == nil {
		return nil, nil, errors.Errorf("invalid empty return") // shouldn't happen
	}

	return c.final.ImmutableRef, c.exporterAttr, nil
}
