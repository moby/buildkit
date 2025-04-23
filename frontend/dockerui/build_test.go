package dockerui

import (
	"testing"

	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

func TestNormalizePlatform(t *testing.T) {
	testCases := []struct {
		p, imgP  ocispecs.Platform
		expected exptypes.Platform
	}{
		{
			p: ocispecs.Platform{
				Architecture: "arm64",
				OS:           "linux",
				Variant:      "v8",
			},
			imgP: ocispecs.Platform{
				Architecture: "arm64",
				OS:           "linux",
			},
			expected: exptypes.Platform{
				ID: "linux/arm64", // Not "linux/arm64/v8" https://github.com/moby/buildkit/issues/5915
				Platform: ocispecs.Platform{
					Architecture: "arm64",
					OS:           "linux",
				},
			},
		},
		// TODO: cover Windows https://github.com/moby/buildkit/pull/5837
	}

	for _, tc := range testCases {
		require.Equal(t, tc.expected, normalizePlatform(tc.p, tc.imgP))
	}
}
