package frontend

import (
	digest "github.com/opencontainers/go-digest"
	"golang.org/x/net/context"

	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/solver/pb"
)

type Frontend interface {
	Solve(ctx context.Context, llbBridge FrontendLLBBridge, opt map[string]string) (cache.ImmutableRef, map[string]interface{}, error)
}

type FrontendLLBBridge interface {
	Solve(ctx context.Context, def *pb.Definition) (cache.ImmutableRef, error)
	ResolveImageConfig(ctx context.Context, ref string) (digest.Digest, []byte, error)
}
