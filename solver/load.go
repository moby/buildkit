package solver

import (
	"strings"

	"github.com/moby/buildkit/solver/pb"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
)

func LoadLLB(ops [][]byte) (Vertex, error) {
	if len(ops) == 0 {
		return nil, errors.New("invalid empty definition")
	}

	allOps := make(map[digest.Digest]*pb.Op)

	var lastOp *pb.Op
	var lastDigest digest.Digest

	for _, dt := range ops {
		var op pb.Op
		if err := (&op).Unmarshal(dt); err != nil {
			return nil, errors.Wrap(err, "failed to parse llb proto op")
		}
		lastOp = &op
		lastDigest = digest.FromBytes(dt)
		allOps[lastDigest] = &op
	}

	delete(allOps, lastDigest) // avoid loops

	cache := make(map[digest.Digest]*vertex)

	// TODO: validate the connections
	return loadLLBVertexRecursive(lastDigest, lastOp, allOps, cache)
}

func toInternalVertex(v Vertex) *vertex {
	cache := make(map[digest.Digest]*vertex)
	return loadInternalVertexHelper(v, cache)
}

func loadInternalVertexHelper(v Vertex, cache map[digest.Digest]*vertex) *vertex {
	if v, ok := cache[v.Digest()]; ok {
		return v
	}
	vtx := &vertex{sys: v.Sys(), digest: v.Digest(), name: v.Name()}
	for _, in := range v.Inputs() {
		vv := loadInternalVertexHelper(in.Vertex, cache)
		vtx.inputs = append(vtx.inputs, &input{index: in.Index, vertex: vv})
	}
	vtx.initClientVertex()
	cache[v.Digest()] = vtx
	return vtx
}

func loadLLBVertexRecursive(dgst digest.Digest, op *pb.Op, all map[digest.Digest]*pb.Op, cache map[digest.Digest]*vertex) (*vertex, error) {
	if v, ok := cache[dgst]; ok {
		return v, nil
	}
	vtx := &vertex{sys: op.Op, digest: dgst, name: llbOpName(op)}
	for _, in := range op.Inputs {
		dgst := digest.Digest(in.Digest)
		op, ok := all[dgst]
		if !ok {
			return nil, errors.Errorf("failed to find %s", in)
		}
		sub, err := loadLLBVertexRecursive(dgst, op, all, cache)
		if err != nil {
			return nil, err
		}
		vtx.inputs = append(vtx.inputs, &input{index: Index(in.Index), vertex: sub})
	}
	vtx.initClientVertex()
	cache[dgst] = vtx
	return vtx, nil
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
