package client

import (
	"testing"

	"github.com/moby/buildkit/solver/pb"
	"github.com/stretchr/testify/require"
)

func TestSolveRequestCloneCopiesFrontendInputs(t *testing.T) {
	req := SolveRequest{
		FrontendOpt: map[string]string{
			"context:base": "input:base",
		},
		FrontendInputs: map[string]*pb.Definition{
			"base": {
				Def: [][]byte{[]byte("def")},
				Metadata: map[string]*pb.OpMetadata{
					"dgst": {},
				},
			},
			"nil": nil,
		},
	}

	clone := req.Clone()

	require.Contains(t, clone.FrontendInputs, "base")
	require.Contains(t, clone.FrontendInputs, "nil")
	require.Nil(t, clone.FrontendInputs["nil"])
	require.NotSame(t, req.FrontendInputs["base"], clone.FrontendInputs["base"])
	require.Equal(t, req.FrontendInputs["base"], clone.FrontendInputs["base"])

	clone.FrontendOpt["context:base"] = "changed"
	clone.FrontendInputs["base"].Metadata["dgst"] = &pb.OpMetadata{
		Description: map[string]string{"name": "changed"},
	}

	require.Equal(t, "input:base", req.FrontendOpt["context:base"])
	require.Empty(t, req.FrontendInputs["base"].Metadata["dgst"].Description)
}
