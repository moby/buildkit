package ops

import (
	"testing"

	"github.com/moby/buildkit/solver"
	"github.com/moby/buildkit/solver/pb"
	digest "github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
)

func TestNewMergeOpValidatesOuterInputCount(t *testing.T) {
	v := &testVertex{
		name: "merge",
		inputs: []solver.Edge{
			{},
			{},
		},
	}

	_, err := NewMergeOp(v, &pb.Op_Merge{Merge: &pb.MergeOp{
		Inputs: []*pb.MergeInput{{Input: 0}},
	}}, nil)
	require.ErrorContains(t, err, "invalid merge op with 1 inner inputs and 2 outer inputs")
}

func TestMergeOpCacheMapUsesValidatedInputCount(t *testing.T) {
	v := &testVertex{
		name: "merge",
		inputs: []solver.Edge{
			{},
			{},
		},
	}

	op, err := NewMergeOp(v, &pb.Op_Merge{Merge: &pb.MergeOp{
		Inputs: []*pb.MergeInput{{Input: 0}, {Input: 1}},
	}}, nil)
	require.NoError(t, err)

	cm, done, err := op.CacheMap(t.Context(), testJobContext(t), 0)
	require.NoError(t, err)
	require.True(t, done)
	require.Len(t, cm.Deps, len(v.inputs))
}

type testVertex struct {
	name   string
	inputs []solver.Edge
}

func (v *testVertex) Digest() digest.Digest {
	return digest.FromBytes([]byte(v.name))
}

func (v *testVertex) Sys() any {
	return v.name
}

func (v *testVertex) Options() solver.VertexOptions {
	return solver.VertexOptions{}
}

func (v *testVertex) Inputs() []solver.Edge {
	return v.inputs
}

func (v *testVertex) Name() string {
	return v.name
}
