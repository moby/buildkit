package frontend

import (
	"context"
	"io"

	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/executor"
	gatewayclient "github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/solver"
	digest "github.com/opencontainers/go-digest"
	specs "github.com/opencontainers/image-spec/specs-go/v1"
)

type Frontend interface {
	Solve(ctx context.Context, llb FrontendLLBBridge, opt map[string]string) (map[string]solver.CachedResult, map[string][]byte, error)
}

type FrontendLLBBridge interface {
	Solve(ctx context.Context, req SolveRequest) (map[string]solver.CachedResult, map[string][]byte, error)
	ResolveImageConfig(ctx context.Context, ref string, platform *specs.Platform) (digest.Digest, []byte, error)
	Exec(ctx context.Context, meta executor.Meta, rootfs cache.ImmutableRef, stdin io.ReadCloser, stdout, stderr io.WriteCloser) error
}

type SolveRequest = gatewayclient.SolveRequest

type WorkerInfos interface {
	WorkerInfos() []client.WorkerInfo
}
