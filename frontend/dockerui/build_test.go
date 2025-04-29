package dockerui

import (
	"testing"

	"github.com/containerd/platforms"
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
		{
			p: ocispecs.Platform{
				Architecture: "arm64",
				OS:           "linux",
				Variant:      "v8",
			},
			imgP: ocispecs.Platform{
				Architecture: "arm64",
				OS:           "linux",
				Variant:      "v8",
			},
			expected: exptypes.Platform{
				ID: "linux/arm64",
				Platform: ocispecs.Platform{
					Architecture: "arm64",
					OS:           "linux",
				},
			},
		},
		{
			p: ocispecs.Platform{
				Architecture: "amd64",
				OS:           "windows",
			},
			imgP: ocispecs.Platform{
				Architecture: "amd64",
				OS:           "windows",
				OSVersion:    "10.0.19041.0",
			},
			expected: exptypes.Platform{
				ID: "windows/amd64",
				Platform: ocispecs.Platform{
					Architecture: "amd64",
					OS:           "windows",
					OSVersion:    "10.0.19041.0",
				},
			},
		},
		{
			p: ocispecs.Platform{
				Architecture: "amd64",
				OS:           "windows",
				OSVersion:    "10.0.19041.0",
			},
			imgP: ocispecs.Platform{
				Architecture: "amd64",
				OS:           "windows",
				OSVersion:    "11.0.22000.0",
			},
			expected: exptypes.Platform{
				ID: "windows(10.0.19041.0)/amd64",
				Platform: ocispecs.Platform{
					Architecture: "amd64",
					OS:           "windows",
					OSVersion:    "10.0.19041.0",
				},
			},
		},
	}

	for _, tc := range testCases {
		require.Equal(t, tc.expected, makeExportPlatform(tc.p, tc.imgP))
		// the ID needs to always be formatall(normalize(p))
		require.Equal(t, platforms.FormatAll(platforms.Normalize(tc.p)), tc.expected.ID)
	}
}
