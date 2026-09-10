package exptypes

import (
	"encoding/json"
	"testing"

	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

func TestParsePlatforms(t *testing.T) {
	img := ocispecs.Image{
		Platform: ocispecs.Platform{
			OS:           "linux",
			Architecture: "arm64",
		},
	}
	dt, err := json.Marshal(img)
	require.NoError(t, err)

	meta := map[string][]byte{
		ExporterImageConfigKey + "/linux/arm64": dt,
	}

	ps, err := ParsePlatforms(meta)
	require.NoError(t, err)
	require.Equal(t, 1, len(ps.Platforms))
	require.Equal(t, "linux", ps.Platforms[0].Platform.OS)
	require.Equal(t, "arm64", ps.Platforms[0].Platform.Architecture)
}
