package llb

import (
	"testing"

	"github.com/moby/buildkit/solver/pb"
	"github.com/stretchr/testify/require"
)

func TestPassthroughRequiresMarshal(t *testing.T) {
	t.Parallel()

	base := Image("example.com/base:latest")
	depA := Image("example.com/dep:a")
	depB := Image("example.com/dep:b")

	def, err := base.Requires("test.requires", depA, depB).Marshal(t.Context())
	require.NoError(t, err)

	op := requirePassthroughVertex(t, def)
	require.Len(t, op.Inputs, 3)
	require.Equal(t, "test.requires", op.GetPassthrough().GetId())
	require.Equal(t, []int64{0}, op.GetPassthrough().GetOutputs())
}

func TestPassthroughMultipleOutputsMarshal(t *testing.T) {
	t.Parallel()

	op := NewPassthroughOp("test.multi", []PassthroughInput{
		{State: Image("example.com/input:0"), Output: true},
		{State: Image("example.com/input:1")},
		{State: Image("example.com/input:2"), Output: true},
	})

	def, err := NewState(op.OutputAt(1)).Marshal(t.Context())
	require.NoError(t, err)

	p := requirePassthroughVertex(t, def)
	require.Len(t, p.Inputs, 3)
	require.Equal(t, "test.multi", p.GetPassthrough().GetId())
	require.Equal(t, []int64{0, 2}, p.GetPassthrough().GetOutputs())
}

func TestPassthroughRequiresNoDeps(t *testing.T) {
	t.Parallel()

	def, err := Image("example.com/base:latest").Requires("test.empty").Marshal(t.Context())
	require.NoError(t, err)
	require.Nil(t, findPassthroughVertex(t, def))
}

func TestPassthroughRequiresPreservesMetadata(t *testing.T) {
	t.Parallel()

	st := Image("example.com/base:latest").Dir("/work").Requires("test.metadata", Image("example.com/dep:latest"))

	dir, err := st.GetDir(t.Context())
	require.NoError(t, err)
	require.Equal(t, "/work", dir)
}

func TestPassthroughEmptyID(t *testing.T) {
	t.Parallel()

	_, err := Image("example.com/base:latest").Requires("", Image("example.com/dep:latest")).Marshal(t.Context())
	require.Error(t, err)
	require.Contains(t, err.Error(), "passthrough requires an id")
}

func requirePassthroughVertex(t *testing.T, def *Definition) *pb.Op {
	t.Helper()
	op := findPassthroughVertex(t, def)
	require.NotNil(t, op)
	return op
}

func findPassthroughVertex(t *testing.T, def *Definition) *pb.Op {
	t.Helper()

	for _, dt := range def.Def {
		var op pb.Op
		require.NoError(t, op.UnmarshalVT(dt))
		if op.GetPassthrough() != nil {
			return &op
		}
	}
	return nil
}
