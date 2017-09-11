package frontend

import (
	"github.com/moby/buildkit/cache"
	"golang.org/x/net/context"
)

type Frontend interface {
	Solve(ctx context.Context, llb FrontendLLBBridge, opt map[string]string) (cache.ImmutableRef, error)
	// TODO: return exporter data
}

type FrontendLLBBridge interface {
	Solve(ctx context.Context, vtx [][]byte) (cache.ImmutableRef, error)
	ResolveImageConfig(ctx context.Context, ref string) ([]byte, error)
}
