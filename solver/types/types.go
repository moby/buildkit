package types

import (
	"github.com/moby/buildkit/exporter"
	"github.com/moby/buildkit/frontend"
	"github.com/moby/buildkit/solver/pb"
	digest "github.com/opencontainers/go-digest"
	"golang.org/x/net/context"
)

// These could be also defined in worker

type Ref interface {
	Release(context.Context) error
}

// Op is an implementation for running a vertex
type Op interface {
	// CacheKey returns a persistent cache key for operation.
	CacheKey(context.Context) (digest.Digest, error)
	// ContentMask returns a partial cache checksum with content paths to the
	// inputs. User can combine the content checksum of these paths to get a valid
	// content based cache key.
	ContentMask(context.Context) (digest.Digest, [][]string, error)
	// Run runs an operation and returns the output references.
	Run(ctx context.Context, inputs []Ref) (outputs []Ref, err error)
}

type SolveRequest struct {
	Definition     *pb.Definition
	Frontend       frontend.Frontend
	Exporter       exporter.ExporterInstance
	FrontendOpt    map[string]string
	ExportCacheRef string
	ImportCacheRef string
}

// Vertex is one node in the build graph
type Vertex interface {
	// Digest is a content-addressable vertex identifier
	Digest() digest.Digest
	// Sys returns an internal value that is used to execute the vertex. Usually
	// this is capured by the operation resolver method during solve.
	Sys() interface{}
	// FIXME(AkihiroSuda): we should not import pb pkg here.
	Metadata() *pb.OpMetadata
	// Array of vertexes current vertex depends on.
	Inputs() []Input
	Name() string // change this to general metadata
}

type Index int

// Input is an pointer to a single reference from a vertex by an index.
type Input struct {
	Index  Index
	Vertex Vertex
}
