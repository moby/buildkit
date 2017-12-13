package solver

import (
	"strings"

	"github.com/moby/buildkit/solver/pb"
	vtxpkg "github.com/moby/buildkit/solver/vertex"
	digest "github.com/opencontainers/go-digest"
)

func newVertex(dgst digest.Digest, op *pb.Op, opMeta *pb.OpMetadata, load func(digest.Digest) (interface{}, error)) (*vertex, error) {
	vtx := &vertex{sys: op.Op, metadata: opMeta, digest: dgst, name: llbOpName(op)}
	for _, in := range op.Inputs {
		sub, err := load(in.Digest)
		if err != nil {
			return nil, err
		}
		vtx.inputs = append(vtx.inputs, &input{index: vtxpkg.Index(in.Index), vertex: sub.(*vertex)})
	}
	vtx.initClientVertex()
	return vtx, nil
}

func toInternalVertex(v vtxpkg.Vertex) *vertex {
	cache := make(map[digest.Digest]*vertex)
	return loadInternalVertexHelper(v, cache)
}

func loadInternalVertexHelper(v vtxpkg.Vertex, cache map[digest.Digest]*vertex) *vertex {
	if v, ok := cache[v.Digest()]; ok {
		return v
	}
	vtx := &vertex{sys: v.Sys(), metadata: v.Metadata(), digest: v.Digest(), name: v.Name()}
	for _, in := range v.Inputs() {
		vv := loadInternalVertexHelper(in.Vertex, cache)
		vtx.inputs = append(vtx.inputs, &input{index: in.Index, vertex: vv})
	}
	vtx.initClientVertex()
	cache[v.Digest()] = vtx
	return vtx
}

func llbOpName(op *pb.Op) string {
	switch op := op.Op.(type) {
	case *pb.Op_Source:
		return op.Source.Identifier
	case *pb.Op_Exec:
		return strings.Join(op.Exec.Meta.Args, " ")
	case *pb.Op_Build:
		return "build"
	default:
		return "unknown"
	}
}
