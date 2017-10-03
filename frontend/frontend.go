package frontend

import (
	"io"

	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/worker"
	digest "github.com/opencontainers/go-digest"
	"golang.org/x/net/context"
)

type Frontend interface {
	Solve(ctx context.Context, llb FrontendLLBBridge, opt map[string]string) (cache.ImmutableRef, map[string][]byte, error)
}

type FrontendLLBBridge interface {
	Solve(ctx context.Context, def *pb.Definition, frontend string, opts map[string]string) (cache.ImmutableRef, map[string][]byte, error)
	ResolveImageConfig(ctx context.Context, ref string) (digest.Digest, []byte, error)
	Exec(ctx context.Context, meta worker.Meta, rootfs cache.ImmutableRef, stdin io.ReadCloser, stdout, stderr io.WriteCloser) error
}
