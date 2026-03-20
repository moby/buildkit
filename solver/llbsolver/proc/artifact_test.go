package proc

import (
	"encoding/json"
	"testing"

	"github.com/containerd/platforms"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	"github.com/moby/buildkit/frontend"
	artifactfrontend "github.com/moby/buildkit/frontend/artifact"
	"github.com/moby/buildkit/solver/llbsolver"
	"github.com/moby/buildkit/solver/llbsolver/provenance"
	"github.com/stretchr/testify/require"
)

func TestUpdateArtifactProvenanceAddsPostprocess(t *testing.T) {
	res := &llbsolver.Result{
		Result: &frontend.Result{},
		Provenance: &provenance.Result{
			Ref: &provenance.Capture{
				Frontend: "dockerfile.v0",
				Args: map[string]string{
					"target": "release",
				},
			},
		},
	}

	postRes := &llbsolver.Result{
		Result: &frontend.Result{},
		Provenance: &provenance.Result{
			Ref: &provenance.Capture{
				Frontend: artifactfrontend.Name,
				Args: map[string]string{
					"type": "artifact",
				},
			},
		},
	}

	require.NoError(t, updateArtifactProvenance(res, postRes))
	require.NotNil(t, res.Provenance)
	require.NotNil(t, res.Provenance.Ref)
	require.Equal(t, "dockerfile.v0", res.Provenance.Ref.Frontend)
	require.Equal(t, "release", res.Provenance.Ref.Args["target"])
	require.Len(t, res.Provenance.Ref.Postprocess, 1)
	require.Equal(t, artifactfrontend.Name, res.Provenance.Ref.Postprocess[0].Frontend)
	require.Equal(t, "artifact", res.Provenance.Ref.Postprocess[0].Args["type"])
}

func TestNormalizeArtifactResultMetadataPreservesSinglePlatformMapping(t *testing.T) {
	ps := exptypes.Platforms{
		Platforms: []exptypes.Platform{{
			ID:       platforms.Format(platforms.MustParse("linux/arm64")),
			Platform: platforms.MustParse("linux/arm64"),
		}},
	}
	psJSON, err := json.Marshal(ps)
	require.NoError(t, err)

	meta := map[string][]byte{
		exptypes.ExporterPlatformsKey:                              psJSON,
		exptypes.ExporterImageConfigKey + "/" + ps.Platforms[0].ID: []byte("config"),
	}

	require.NoError(t, normalizeArtifactResultMetadata(meta))
	require.Equal(t, []byte("config"), meta[exptypes.ExporterImageConfigKey])
	require.Equal(t, psJSON, meta[exptypes.ExporterPlatformsKey])
}
