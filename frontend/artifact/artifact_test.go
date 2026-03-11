package artifact

import (
	"testing"

	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	"github.com/stretchr/testify/require"
)

func TestNormalizeAndValidateArtifactMetadata(t *testing.T) {
	t.Run("normalizes relative paths", func(t *testing.T) {
		layers, err := normalizeAndValidateArtifactMetadata("application/vnd.test.config.v1+json", []exptypes.ArtifactLayer{{
			Path:      "./subdir/../artifact.txt",
			MediaType: "application/vnd.test.layer.v1",
		}})
		require.NoError(t, err)
		require.Len(t, layers, 1)
		require.Equal(t, "artifact.txt", layers[0].Path)
	})

	t.Run("rejects missing config media type", func(t *testing.T) {
		_, err := normalizeAndValidateArtifactMetadata("", nil)
		require.EqualError(t, err, "artifact frontend: config descriptor mediaType is required")
	})

	t.Run("rejects missing layer path", func(t *testing.T) {
		_, err := normalizeAndValidateArtifactMetadata("application/vnd.test.config.v1+json", []exptypes.ArtifactLayer{{
			MediaType: "application/vnd.test.layer.v1",
		}})
		require.EqualError(t, err, "artifact frontend: invalid layer 0 path: path is required")
	})

	t.Run("rejects absolute paths", func(t *testing.T) {
		_, err := normalizeAndValidateArtifactMetadata("application/vnd.test.config.v1+json", []exptypes.ArtifactLayer{{
			Path:      "/artifact.txt",
			MediaType: "application/vnd.test.layer.v1",
		}})
		require.EqualError(t, err, "artifact frontend: invalid layer 0 path: path must be relative to the artifact input root")
	})

	t.Run("rejects parent traversal", func(t *testing.T) {
		_, err := normalizeAndValidateArtifactMetadata("application/vnd.test.config.v1+json", []exptypes.ArtifactLayer{{
			Path:      "../artifact.txt",
			MediaType: "application/vnd.test.layer.v1",
		}})
		require.EqualError(t, err, "artifact frontend: invalid layer 0 path: path must be relative to the artifact input root")
	})

	t.Run("rejects root path", func(t *testing.T) {
		_, err := normalizeAndValidateArtifactMetadata("application/vnd.test.config.v1+json", []exptypes.ArtifactLayer{{
			Path:      ".",
			MediaType: "application/vnd.test.layer.v1",
		}})
		require.EqualError(t, err, "artifact frontend: invalid layer 0 path: path must reference a file within the artifact input root")
	})

	t.Run("rejects missing layer media type", func(t *testing.T) {
		_, err := normalizeAndValidateArtifactMetadata("application/vnd.test.config.v1+json", []exptypes.ArtifactLayer{{
			Path: "artifact.txt",
		}})
		require.EqualError(t, err, "artifact frontend: layer 0 descriptor mediaType is required")
	})
}
