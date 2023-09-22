package ops

import (
	"testing"

	"github.com/moby/buildkit/solver"
	"github.com/moby/buildkit/solver/pb"
	digest "github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
)

func TestDedupPaths(t *testing.T) {
	res := dedupePaths([]string{"Gemfile", "Gemfile/foo"})
	require.Equal(t, []string{"Gemfile"}, res)

	res = dedupePaths([]string{"Gemfile/bar", "Gemfile/foo"})
	require.Equal(t, []string{"Gemfile/bar", "Gemfile/foo"}, res)

	res = dedupePaths([]string{"Gemfile", "Gemfile.lock"})
	require.Equal(t, []string{"Gemfile", "Gemfile.lock"}, res)

	res = dedupePaths([]string{"Gemfile.lock", "Gemfile"})
	require.Equal(t, []string{"Gemfile", "Gemfile.lock"}, res)

	res = dedupePaths([]string{"foo", "Gemfile", "Gemfile/foo"})
	require.Equal(t, []string{"Gemfile", "foo"}, res)

	res = dedupePaths([]string{"foo/bar/baz", "foo/bara", "foo/bar/bax", "foo/bar"})
	require.Equal(t, []string{"foo/bar", "foo/bara"}, res)
}

func TestExecOp_getMountDeps(t *testing.T) {
	v1 := &vertex{name: "local://context"}
	v2 := &vertex{
		name: "foo",
		inputs: []solver.Edge{
			{Vertex: v1, Index: 0},
		},
	}
	op2, err := NewExecOp(v2, &pb.Op_Exec{
		Exec: &pb.ExecOp{
			Meta: &pb.Meta{
				Args: []string{"/bin/bash", "-l"},
			},
			Mounts: []*pb.Mount{
				{
					Input:    pb.Empty,
					Dest:     "/",
					Readonly: true,
					Output:   pb.SkipOutput,
				},
				{
					Input:    pb.InputIndex(0),
					Selector: "b.txt",
					Dest:     "/test/b.txt",
					Output:   pb.SkipOutput,
				},
				{
					Input:  pb.InputIndex(0),
					Dest:   "/test/data",
					Output: pb.SkipOutput,
				},
			},
		},
	}, nil, nil, nil, nil, nil, nil)
	require.NoError(t, err)

	deps, err := op2.getMountDeps()
	require.NoError(t, err)

	require.Len(t, deps, 1)
	require.Len(t, deps[0].Selectors, 0)
	require.False(t, deps[0].NoContentBasedHash)
}

type vertex struct {
	name   string
	inputs []solver.Edge
}

func (v *vertex) Digest() digest.Digest {
	return digest.FromString(v.name)
}

func (v *vertex) Sys() interface{} {
	return v
}

func (v *vertex) Options() solver.VertexOptions {
	return solver.VertexOptions{}
}

func (v *vertex) Inputs() []solver.Edge {
	return v.inputs
}

func (v *vertex) Name() string {
	return v.name
}
