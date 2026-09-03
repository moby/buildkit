package opsutils

import (
	"testing"

	"github.com/moby/buildkit/solver/pb"
	"github.com/stretchr/testify/require"
)

func TestValidateDiffNilInputs(t *testing.T) {
	require.Error(t, Validate(&pb.Op{Op: &pb.Op_Diff{Diff: &pb.DiffOp{Lower: nil, Upper: &pb.UpperDiffInput{Input: -1}}}}))
	require.Error(t, Validate(&pb.Op{Op: &pb.Op_Diff{Diff: &pb.DiffOp{Lower: &pb.LowerDiffInput{Input: -1}, Upper: nil}}}))
	require.NoError(t, Validate(&pb.Op{Op: &pb.Op_Diff{Diff: &pb.DiffOp{Lower: &pb.LowerDiffInput{Input: -1}, Upper: &pb.UpperDiffInput{Input: -1}}}}))
}

func TestValidateMergeInputCount(t *testing.T) {
	require.ErrorContains(t, Validate(&pb.Op{
		Inputs: []*pb.Input{{}, {}},
		Op: &pb.Op_Merge{Merge: &pb.MergeOp{
			Inputs: []*pb.MergeInput{{Input: 0}},
		}},
	}), "invalid merge op with 1 inner inputs and 2 outer inputs")

	require.NoError(t, Validate(&pb.Op{
		Inputs: []*pb.Input{{}, {}},
		Op: &pb.Op_Merge{Merge: &pb.MergeOp{
			Inputs: []*pb.MergeInput{{Input: 0}, {Input: 1}},
		}},
	}))
}
