package llbsolver

import (
	"context"
	_ "embed"
	"fmt"
	"testing"

	"github.com/moby/buildkit/solver/pb"
	digest "github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecomputeDigests(t *testing.T) {
	op1 := &pb.Op{
		Op: &pb.Op_Source{
			Source: &pb.SourceOp{
				Identifier: "docker-image://docker.io/library/busybox:latest",
			},
		},
	}
	oldData, err := op1.Marshal()
	require.NoError(t, err)
	oldDigest := digest.FromBytes(oldData)

	op1.GetOp().(*pb.Op_Source).Source.Identifier = "docker-image://docker.io/library/busybox:1.31.1"
	newData, err := op1.Marshal()
	require.NoError(t, err)
	newDigest := digest.FromBytes(newData)

	op2 := &pb.Op{
		Inputs: []*pb.Input{
			{Digest: string(oldDigest)}, // Input is the old digest, this should be updated after recomputeDigests
		},
	}
	op2Data, err := op2.Marshal()
	require.NoError(t, err)
	op2Digest := digest.FromBytes(op2Data)

	all := map[digest.Digest]*op{
		newDigest: {Op: op1},
		op2Digest: {Op: op2},
	}
	visited := map[digest.Digest]digest.Digest{oldDigest: newDigest}

	updated, err := recomputeDigests(context.Background(), all, visited, op2Digest)
	require.NoError(t, err)
	require.Len(t, visited, 2)
	require.Len(t, all, 2)
	assert.Equal(t, op1, all[newDigest].Op)
	require.Equal(t, newDigest, visited[oldDigest])
	require.Equal(t, op1, all[newDigest].Op)
	assert.Equal(t, op2, all[updated].Op)
	require.Equal(t, newDigest, digest.Digest(op2.Inputs[0].Digest))
	assert.NotEqual(t, op2Digest, updated)
}

//go:embed testdata/gogoproto.data
var gogoprotoData []byte

func TestIngestDigest(t *testing.T) {
	op1 := &pb.Op{
		Op: &pb.Op_Source{
			Source: &pb.SourceOp{
				Identifier: "docker-image://docker.io/library/busybox:latest",
			},
		},
	}
	op1Data, err := op1.Marshal()
	require.NoError(t, err)
	op1Digest := digest.FromBytes(op1Data)

	op2 := &pb.Op{
		Inputs: []*pb.Input{
			{Digest: string(op1Digest)}, // Input is the old digest, this should be updated after recomputeDigests
		},
	}
	op2Data, err := op2.Marshal()
	require.NoError(t, err)
	op2Digest := digest.FromBytes(op2Data)

	var def pb.Definition
	err = def.Unmarshal(gogoprotoData)
	require.NoError(t, err)
	require.Len(t, def.Def, 2)

	// Read the definition from the test data and ensure it uses the
	// canonical digests after recompute.
	var lastDgst digest.Digest
	all := map[digest.Digest]*op{}
	for _, in := range def.Def {
		opNew := new(pb.Op)
		err := opNew.Unmarshal(in)
		require.NoError(t, err)

		lastDgst = digest.FromBytes(in)
		all[lastDgst] = &op{Op: opNew}
	}
	fmt.Println(all, lastDgst)

	visited := map[digest.Digest]digest.Digest{}
	newDgst, err := recomputeDigests(context.Background(), all, visited, lastDgst)
	require.NoError(t, err)
	require.Len(t, visited, 2)
	require.Equal(t, op2Digest, newDgst)
	require.Equal(t, op2Digest, visited[newDgst])
	delete(visited, newDgst)

	// Last element should correspond to op1.
	// The old digest doesn't really matter.
	require.Len(t, visited, 1)
	for _, newDgst := range visited {
		require.Equal(t, op1Digest, newDgst)
	}
}
