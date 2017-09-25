package frontend

import (
	"github.com/moby/buildkit/cache"
	digest "github.com/opencontainers/go-digest"
	"golang.org/x/net/context"
)

type Frontend interface {
	Solve(ctx context.Context, llb FrontendLLBBridge, opt map[string]string) (cache.ImmutableRef, map[string]interface{}, error)
}

type FrontendLLBBridge interface {
	Solve(ctx context.Context, vtx [][]byte) (cache.ImmutableRef, error)
	ResolveImageConfig(ctx context.Context, ref string) (digest.Digest, []byte, error)
}
