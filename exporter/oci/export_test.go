package oci

import (
	"context"
	"testing"

	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	"github.com/stretchr/testify/require"
)

func TestResolveDefaultsOCIArtifactEnabled(t *testing.T) {
	for _, variant := range []ExporterVariant{VariantOCI, VariantDocker} {
		exp, err := New(Opt{Variant: variant})
		require.NoError(t, err)

		instAny, err := exp.Resolve(context.Background(), 0, map[string]string{})
		require.NoError(t, err)

		inst, ok := instAny.(*imageExporterInstance)
		require.True(t, ok)
		require.True(t, inst.opts.OCIArtifact)
	}
}

func TestResolveAllowsDisablingOCIArtifact(t *testing.T) {
	exp, err := New(Opt{Variant: VariantDocker})
	require.NoError(t, err)

	instAny, err := exp.Resolve(context.Background(), 0, map[string]string{
		string(exptypes.OptKeyOCIArtifact): "false",
	})
	require.NoError(t, err)

	inst, ok := instAny.(*imageExporterInstance)
	require.True(t, ok)
	require.False(t, inst.opts.OCIArtifact)
}

