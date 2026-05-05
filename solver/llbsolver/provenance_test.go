package llbsolver

import (
	"testing"

	"github.com/moby/buildkit/solver/errdefs"
	"github.com/moby/buildkit/solver/llbsolver/provenance"
	provenancetypes "github.com/moby/buildkit/solver/llbsolver/provenance/types"
	"github.com/stretchr/testify/require"
)

func TestIncompleteMaterialsErrorIncludesLocalSources(t *testing.T) {
	err := incompleteMaterialsError(&provenance.Capture{
		Sources: provenancetypes.Sources{
			Local: []provenancetypes.LocalSource{
				{Name: "context"},
			},
		},
	})

	var materialsErr *errdefs.ProvenanceMaterialsIncompleteError
	require.ErrorAs(t, err, &materialsErr)
	require.Len(t, materialsErr.Incomplete, 1)
	require.Equal(t, "context", materialsErr.Incomplete[0].Name)
	require.Equal(t, "local_source", materialsErr.Incomplete[0].Reason)
	require.ErrorContains(t, err, "Uncaptured local sources")
	require.ErrorContains(t, err, "context")
}
