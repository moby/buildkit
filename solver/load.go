package solver

import (
	"strings"

	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/source"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
)

func newVertex(dgst digest.Digest, op *pb.Op, opMeta *pb.OpMetadata, load func(digest.Digest) (interface{}, error)) (*vertex, error) {
	vtx := &vertex{sys: op.Op, metadata: opMeta, digest: dgst, name: llbOpName(op)}
	for _, in := range op.Inputs {
		sub, err := load(in.Digest)
		if err != nil {
			return nil, err
		}
		vtx.inputs = append(vtx.inputs, &input{index: Index(in.Index), vertex: sub.(*vertex)})
	}
	vtx.initClientVertex()
	return vtx, nil
}

func toInternalVertex(v Vertex) *vertex {
	cache := make(map[digest.Digest]*vertex)
	return loadInternalVertexHelper(v, cache)
}

func loadInternalVertexHelper(v Vertex, cache map[digest.Digest]*vertex) *vertex {
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

// loadLLB loads LLB.
// fn is executed sequentially.
func loadLLB(def *pb.Definition, fn func(digest.Digest, *pb.Op, func(digest.Digest) (interface{}, error)) (interface{}, error)) (interface{}, Index, error) {
	if len(def.Def) == 0 {
		return nil, 0, errors.New("invalid empty definition")
	}

	allOps := make(map[digest.Digest]*pb.Op)

	var dgst digest.Digest

	for _, dt := range def.Def {
		var op pb.Op
		if err := (&op).Unmarshal(dt); err != nil {
			return nil, 0, errors.Wrap(err, "failed to parse llb proto op")
		}
		dgst = digest.FromBytes(dt)
		allOps[dgst] = &op
	}

	lastOp := allOps[dgst]
	delete(allOps, dgst)
	dgst = lastOp.Inputs[0].Digest

	cache := make(map[digest.Digest]interface{})

	var rec func(dgst digest.Digest) (interface{}, error)
	rec = func(dgst digest.Digest) (interface{}, error) {
		if v, ok := cache[dgst]; ok {
			return v, nil
		}
		v, err := fn(dgst, allOps[dgst], rec)
		if err != nil {
			return nil, err
		}
		cache[dgst] = v
		return v, nil
	}

	v, err := rec(dgst)
	return v, Index(lastOp.Inputs[0].Index), err
}

func llbOpName(op *pb.Op) string {
	switch op := op.Op.(type) {
	case *pb.Op_Source:
		if id, err := source.FromLLB(op); err == nil {
			if id, ok := id.(*source.LocalIdentifier); ok {
				if len(id.IncludePatterns) == 1 {
					return op.Source.Identifier + " (" + id.IncludePatterns[0] + ")"
				}
			}
		}
		return op.Source.Identifier
	case *pb.Op_Exec:
		return strings.Join(op.Exec.Meta.Args, " ")
	case *pb.Op_Build:
		return "build"
	default:
		return "unknown"
	}
}
