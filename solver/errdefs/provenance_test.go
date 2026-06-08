package errdefs

import (
	"testing"

	"github.com/moby/buildkit/util/grpcerrors"
	"github.com/stretchr/testify/require"
)

func TestProvenanceMaterialsIncompleteRoundTrip(t *testing.T) {
	err := NewProvenanceMaterialsIncomplete([]*ProvenanceMaterialIncomplete{
		{
			Op:     "sha256:abc",
			Name:   "curl https://example.com/missing",
			Method: "GET",
			Uri:    "https://example.com/missing",
			Reason: "unsuccessful_response",
		},
	})

	decoded := grpcerrors.FromGRPC(grpcerrors.ToGRPC(t.Context(), err))
	var pe *ProvenanceMaterialsIncompleteError
	require.ErrorAs(t, decoded, &pe)
	require.Len(t, pe.Incomplete, 1)
	require.Equal(t, "https://example.com/missing", pe.Incomplete[0].Uri)
	require.Equal(t, "unsuccessful_response", pe.Incomplete[0].Reason)
	require.ErrorContains(t, decoded, "provenance materials are incomplete")
}
