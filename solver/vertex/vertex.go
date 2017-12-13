package vertex

import (
	"github.com/moby/buildkit/solver/pb"
	digest "github.com/opencontainers/go-digest"
)

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
