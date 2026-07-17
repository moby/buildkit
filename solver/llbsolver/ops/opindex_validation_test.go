package ops

import (
	"testing"

	"github.com/moby/buildkit/solver/pb"
	"github.com/stretchr/testify/require"
)

// These exercise malicious op input indices that previously panicked the
// daemon (out-of-range / negative slice indexing). They must now return an
// error instead of panicking.

func TestExecMountNegativeInput(t *testing.T) {
	op := &ExecOp{
		op: &pb.ExecOp{
			Meta:   &pb.Meta{Args: []string{"x"}},
			Mounts: []*pb.Mount{{Dest: "/", Input: -2}},
		},
		numInputs: 1,
	}
	require.NotPanics(t, func() {
		_, _, err := op.CacheMap(t.Context(), testJobContext(t), 1)
		require.Error(t, err)
	})
}

func TestFileCopySecondaryNegativeInput(t *testing.T) {
	fo := &pb.FileOp{
		Actions: []*pb.FileAction{
			{
				Input:          0,
				SecondaryInput: -2,
				Output:         0,
				Action: &pb.FileAction_Copy{
					Copy: &pb.FileActionCopy{Src: "/src", Dest: "/dest"},
				},
			},
		},
	}
	f := &fileOp{op: fo, numInputs: 1}
	require.NotPanics(t, func() {
		_, _, err := f.CacheMap(t.Context(), testJobContext(t), 1)
		require.Error(t, err)
	})
}

func TestFileOwnerUnboundedInput(t *testing.T) {
	fo := &pb.FileOp{
		Actions: []*pb.FileAction{
			{
				Input:          0,
				SecondaryInput: -1,
				Output:         0,
				Action: &pb.FileAction_Mkdir{
					Mkdir: &pb.FileActionMkDir{
						Path: "/foo",
						Mode: 0700,
						Owner: &pb.ChownOpt{
							User: &pb.UserOpt{User: &pb.UserOpt_ByName{ByName: &pb.NamedUserOpt{Input: 1000000}}},
						},
					},
				},
			},
		},
	}
	f := &fileOp{op: fo, numInputs: 1}
	require.NotPanics(t, func() {
		_, _, err := f.CacheMap(t.Context(), testJobContext(t), 1)
		require.Error(t, err)
	})
}

func TestFileOwnerInputWithoutInputs(t *testing.T) {
	// numInputs == 0 edge: owner references input 0 but there are no inputs.
	fo := &pb.FileOp{
		Actions: []*pb.FileAction{
			{
				Input:          -1,
				SecondaryInput: -1,
				Output:         0,
				Action: &pb.FileAction_Mkdir{
					Mkdir: &pb.FileActionMkDir{
						Path: "/foo",
						Mode: 0700,
						Owner: &pb.ChownOpt{
							User: &pb.UserOpt{User: &pb.UserOpt_ByName{ByName: &pb.NamedUserOpt{Input: 0}}},
						},
					},
				},
			},
		},
	}
	f := &fileOp{op: fo, numInputs: 0}
	require.NotPanics(t, func() {
		_, _, err := f.CacheMap(t.Context(), testJobContext(t), 1)
		require.Error(t, err)
	})
}
