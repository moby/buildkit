package client

import (
	"testing"

	"github.com/moby/buildkit/client/llb"
	"github.com/stretchr/testify/require"
)

func TestSolveRejectsInvalidLocalExporterMode(t *testing.T) {
	st := llb.Scratch().File(
		llb.Mkfile("fresh.txt", 0600, []byte("fresh")),
	)
	def, err := st.Marshal(t.Context())
	require.NoError(t, err)

	_, err = (&Client{}).Solve(t.Context(), def, SolveOpt{
		Exports: []ExportEntry{
			{
				Type:      ExporterLocal,
				OutputDir: t.TempDir(),
				Attrs: map[string]string{
					"mode": "backup",
				},
			},
		},
	}, nil)
	require.ErrorContains(t, err, `invalid local exporter mode "backup"`)
}
