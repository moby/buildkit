package solver

import (
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/exporter"
	"github.com/moby/buildkit/frontend"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/solver/reference"
	digest "github.com/opencontainers/go-digest"
	"golang.org/x/net/context"
)

// Op is an implementation for running a vertex
type Op interface {
	// CacheKey returns a persistent cache key for operation.
	CacheKey(context.Context) (digest.Digest, error)
	// ContentMask returns a partial cache checksum with content paths to the
	// inputs. User can combine the content checksum of these paths to get a valid
	// content based cache key.
	ContentMask(context.Context) (digest.Digest, [][]string, error)
	// Run runs an operation and returns the output references.
	Run(ctx context.Context, inputs []reference.Ref) (outputs []reference.Ref, err error)
}

type SolveRequest struct {
	Definition     *pb.Definition
	Frontend       frontend.Frontend
	Exporter       exporter.ExporterInstance
	FrontendOpt    map[string]string
	ExportCacheRef string
	ImportCacheRef string
}

type Solver interface {
	Solve(ctx context.Context, id string, req SolveRequest) error
	Status(ctx context.Context, id string, statusChan chan *client.SolveStatus) error
}
