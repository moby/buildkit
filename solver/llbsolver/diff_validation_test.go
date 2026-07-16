package llbsolver

import (
	"testing"

	"github.com/moby/buildkit/solver"
	"github.com/moby/buildkit/solver/pb"
	digest "github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
)

func noopLoad(string) (solver.Vertex, error) { return nil, nil }

// llbOpName previously panicked on malicious diff ops (nil lower/upper input,
// out-of-range input index). It must now return an error instead.

func TestDiffNilLowerName(t *testing.T) {
	pbOp := &pb.Op{
		Op:     &pb.Op_Diff{Diff: &pb.DiffOp{Lower: nil, Upper: &pb.UpperDiffInput{Input: -1}}},
		Inputs: []*pb.Input{},
	}
	require.NotPanics(t, func() {
		_, err := llbOpName(pbOp, noopLoad)
		require.Error(t, err)
	})
}

func TestDiffOOBInputName(t *testing.T) {
	pbOp := &pb.Op{
		Op: &pb.Op_Diff{Diff: &pb.DiffOp{
			Lower: &pb.LowerDiffInput{Input: 5},
			Upper: &pb.UpperDiffInput{Input: -1},
		}},
		Inputs: []*pb.Input{{Digest: string(digest.FromBytes([]byte("x")))}},
	}
	require.NotPanics(t, func() {
		_, err := llbOpName(pbOp, noopLoad)
		require.Error(t, err)
	})
}

func TestDiffInputWithoutInputsName(t *testing.T) {
	// input index 0 referenced but no inputs declared.
	pbOp := &pb.Op{
		Op: &pb.Op_Diff{Diff: &pb.DiffOp{
			Lower: &pb.LowerDiffInput{Input: 0},
			Upper: &pb.UpperDiffInput{Input: -1},
		}},
		Inputs: []*pb.Input{},
	}
	require.NotPanics(t, func() {
		_, err := llbOpName(pbOp, noopLoad)
		require.Error(t, err)
	})
}
