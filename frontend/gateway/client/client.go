package client

import (
	"context"

	"github.com/moby/buildkit/solver/pb"
	digest "github.com/opencontainers/go-digest"
)

// TODO: make this take same options as LLBBridge. Add Return()
type Client interface {
	Solve(ctx context.Context, req SolveRequest, exporterAttr map[string][]byte, final bool) (Reference, error)
	ResolveImageConfig(ctx context.Context, ref string) (digest.Digest, []byte, error)
	Opts() map[string]string
	SessionID() string
}

type Reference interface {
	ReadFile(ctx context.Context, fp string) ([]byte, error)
}

// SolveRequest is same as frontend.SolveRequest but avoiding dependency
type SolveRequest struct {
	Definition      *pb.Definition
	Frontend        string
	FrontendOpt     map[string]string
	ImportCacheRefs []string
}
