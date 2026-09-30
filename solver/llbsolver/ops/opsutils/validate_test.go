package opsutils

import (
	"fmt"
	"testing"

	"github.com/moby/buildkit/solver/pb"
	"github.com/stretchr/testify/require"
)

func TestValidateDiffNilInputs(t *testing.T) {
	require.Error(t, Validate(&pb.Op{Op: &pb.Op_Diff{Diff: &pb.DiffOp{Lower: nil, Upper: &pb.UpperDiffInput{Input: -1}}}}))
	require.Error(t, Validate(&pb.Op{Op: &pb.Op_Diff{Diff: &pb.DiffOp{Lower: &pb.LowerDiffInput{Input: -1}, Upper: nil}}}))
	require.NoError(t, Validate(&pb.Op{Op: &pb.Op_Diff{Diff: &pb.DiffOp{Lower: &pb.LowerDiffInput{Input: -1}, Upper: &pb.UpperDiffInput{Input: -1}}}}))
}

func TestValidateSourceInputCount(t *testing.T) {
	require.ErrorContains(t, Validate(&pb.Op{
		Inputs: []*pb.Input{{}},
		Op:     &pb.Op_Source{Source: &pb.SourceOp{}},
	}), "invalid source op with 1 inputs")

	require.NoError(t, Validate(&pb.Op{
		Op: &pb.Op_Source{Source: &pb.SourceOp{}},
	}))
}

func TestValidateDiffInputCount(t *testing.T) {
	tests := []struct {
		name       string
		inputs     []*pb.Input
		lower      int64
		upper      int64
		wantErr    bool
		innerCount int
	}{
		{name: "scratch to scratch", lower: int64(pb.Empty), upper: int64(pb.Empty)},
		{name: "scratch to input", inputs: []*pb.Input{{}}, lower: int64(pb.Empty), upper: 0},
		{name: "input to scratch", inputs: []*pb.Input{{}}, lower: 0, upper: int64(pb.Empty)},
		{name: "input to input", inputs: []*pb.Input{{}, {}}, lower: 0, upper: 1},
		{name: "extra outer input", inputs: []*pb.Input{{}}, lower: int64(pb.Empty), upper: int64(pb.Empty), wantErr: true, innerCount: 0},
		{name: "missing outer input", lower: int64(pb.Empty), upper: 0, wantErr: true, innerCount: 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate(&pb.Op{
				Inputs: tc.inputs,
				Op: &pb.Op_Diff{Diff: &pb.DiffOp{
					Lower: &pb.LowerDiffInput{Input: tc.lower},
					Upper: &pb.UpperDiffInput{Input: tc.upper},
				}},
			})
			if tc.wantErr {
				require.ErrorContains(t, err, fmt.Sprintf("invalid diff op with %d inner inputs and %d outer inputs", tc.innerCount, len(tc.inputs)))
				return
			}
			require.NoError(t, err)
		})
	}
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
