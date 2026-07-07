package client

import (
	"context"
	"testing"

	"github.com/moby/buildkit/client/llb"
	gateway "github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/solver/errdefs"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/stretchr/testify/require"
)

func testSourceMap(t *testing.T, sb integration.Sandbox) {
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	sm1 := llb.NewSourceMap(nil, "foo", "", []byte("data1"))
	sm2 := llb.NewSourceMap(nil, "bar", "", []byte("data2"))

	st := llb.Scratch().Run(
		llb.Shlex("not-exist"),
		sm1.Location([]*pb.Range{{Start: &pb.Position{Line: 7}}}),
		sm2.Location([]*pb.Range{{Start: &pb.Position{Line: 8}}}),
		sm1.Location([]*pb.Range{{Start: &pb.Position{Line: 9}}}),
	)

	def, err := st.Marshal(sb.Context())
	require.NoError(t, err)

	_, err = c.Solve(sb.Context(), def, SolveOpt{}, nil)
	require.Error(t, err)

	srcs := errdefs.Sources(err)
	require.Equal(t, 3, len(srcs))

	// Source errors are wrapped in the order provided as llb.ConstraintOpts, so
	// when they are unwrapped, the first unwrapped error is the last location
	// provided.
	require.Equal(t, "foo", srcs[0].Info.Filename)
	require.Equal(t, []byte("data1"), srcs[0].Info.Data)
	require.Nil(t, srcs[0].Info.Definition)

	require.Equal(t, 1, len(srcs[0].Ranges))
	require.Equal(t, int32(9), srcs[0].Ranges[0].Start.Line)
	require.Equal(t, int32(0), srcs[0].Ranges[0].Start.Character)

	require.Equal(t, "bar", srcs[1].Info.Filename)
	require.Equal(t, []byte("data2"), srcs[1].Info.Data)
	require.Nil(t, srcs[1].Info.Definition)

	require.Equal(t, 1, len(srcs[1].Ranges))
	require.Equal(t, int32(8), srcs[1].Ranges[0].Start.Line)
	require.Equal(t, int32(0), srcs[1].Ranges[0].Start.Character)

	require.Equal(t, "foo", srcs[2].Info.Filename)
	require.Equal(t, []byte("data1"), srcs[2].Info.Data)
	require.Nil(t, srcs[2].Info.Definition)

	require.Equal(t, 1, len(srcs[2].Ranges))
	require.Equal(t, int32(7), srcs[2].Ranges[0].Start.Line)
	require.Equal(t, int32(0), srcs[2].Ranges[0].Start.Character)
}

func testSourceMapFromRef(t *testing.T, sb integration.Sandbox) {
	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	srcState := llb.Scratch().File(
		llb.Mkfile("foo", 0600, []byte("data")))
	sm := llb.NewSourceMap(&srcState, "bar", "mylang", []byte("bardata"))

	frontend := func(ctx context.Context, c gateway.Client) (*gateway.Result, error) {
		st := llb.Scratch().File(
			llb.Mkdir("foo/bar", 0600), // fails because /foo doesn't exist
			sm.Location([]*pb.Range{{Start: &pb.Position{Line: 3, Character: 1}}}),
		)

		def, err := st.Marshal(sb.Context())
		if err != nil {
			return nil, err
		}

		res, err := c.Solve(ctx, gateway.SolveRequest{
			Definition: def.ToPB(),
		})
		if err != nil {
			return nil, err
		}

		ref, err := res.SingleRef()
		if err != nil {
			return nil, err
		}

		st2, err := ref.ToState()
		if err != nil {
			return nil, err
		}

		st = llb.Scratch().File(
			llb.Copy(st2, "foo", "foo2"),
		)

		def, err = st.Marshal(sb.Context())
		if err != nil {
			return nil, err
		}

		return c.Solve(ctx, gateway.SolveRequest{
			Definition: def.ToPB(),
		})
	}

	_, err = c.Build(sb.Context(), SolveOpt{}, "", frontend, nil)
	require.Error(t, err)

	srcs := errdefs.Sources(err)
	require.Equal(t, 1, len(srcs))

	require.Equal(t, "bar", srcs[0].Info.Filename)
	require.Equal(t, "mylang", srcs[0].Info.Language)
	require.Equal(t, []byte("bardata"), srcs[0].Info.Data)
	require.NotNil(t, srcs[0].Info.Definition)

	require.Equal(t, 1, len(srcs[0].Ranges))
	require.Equal(t, int32(3), srcs[0].Ranges[0].Start.Line)
	require.Equal(t, int32(1), srcs[0].Ranges[0].Start.Character)
}
