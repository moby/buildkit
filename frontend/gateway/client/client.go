package client

import (
	"github.com/moby/buildkit/solver/pb"
	digest "github.com/opencontainers/go-digest"
	"golang.org/x/net/context"
)

// TODO: make this take same options as LLBBridge. Add Return()
type Client interface {
	Solve(ctx context.Context, def *pb.Definition, frontend string, exporterAttr map[string][]byte, final bool) (Reference, error)
	ResolveImageConfig(ctx context.Context, ref string) (digest.Digest, []byte, error)
	Opts() map[string]string
	SessionID() string
}

type Reference interface {
	ReadFile(ctx context.Context, fp string) ([]byte, error)
}
