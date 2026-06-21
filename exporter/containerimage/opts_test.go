package containerimage

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestImageCommitOptsSetOCIArtifactDefault(t *testing.T) {
	t.Run("defaults to OCI artifact when OCI mediatypes are enabled", func(t *testing.T) {
		var opts ImageCommitOpts
		_, err := opts.Load(context.Background(), map[string]string{})
		require.NoError(t, err)

		opts.SetOCITypesDefault(true)
		opts.SetOCIArtifactDefault(opts.OCITypesEnabled())

		require.True(t, opts.OCIArtifact)
	})

	t.Run("keeps explicit OCI artifact opt-out", func(t *testing.T) {
		var opts ImageCommitOpts
		_, err := opts.Load(context.Background(), map[string]string{
			"oci-artifact": "false",
		})
		require.NoError(t, err)

		opts.SetOCITypesDefault(true)
		opts.SetOCIArtifactDefault(opts.OCITypesEnabled())

		require.False(t, opts.OCIArtifact)
	})

	t.Run("does not enable OCI artifact when OCI mediatypes are disabled", func(t *testing.T) {
		var opts ImageCommitOpts
		_, err := opts.Load(context.Background(), map[string]string{})
		require.NoError(t, err)

		opts.SetOCITypesDefault(false)
		opts.SetOCIArtifactDefault(opts.OCITypesEnabled())

		require.False(t, opts.OCIArtifact)
	})
}
