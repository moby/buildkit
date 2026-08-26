package verifier

import (
	"encoding/json"
	"testing"

	"github.com/containerd/platforms"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	"github.com/moby/buildkit/solver/result"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

func TestMultiPlatformWindowsVersionInferred(t *testing.T) {
	res := platformResult(t,
		[]string{"linux/amd64", "windows/amd64"},
		[]exptypes.Platform{
			{
				ID:       "linux/amd64",
				Platform: platforms.MustParse("linux/amd64"),
			},
			{
				ID: "windows/amd64",
				Platform: ocispecs.Platform{
					OS:           "windows",
					Architecture: "amd64",
					OSVersion:    "10.0.26100.33296",
				},
			},
		},
	)

	warnings, err := CheckInvalidPlatforms(t.Context(), res)
	require.NoError(t, err)
	require.Empty(t, warnings)
}

func TestSinglePlatformWindowsVersionInferred(t *testing.T) {
	res := platformResult(t,
		[]string{"windows/amd64"},
		[]exptypes.Platform{
			{
				ID: "windows/amd64",
				Platform: ocispecs.Platform{
					OS:           "windows",
					Architecture: "amd64",
					OSVersion:    "10.0.26100.33296",
				},
			},
		},
	)

	warnings, err := CheckInvalidPlatforms(t.Context(), res)
	require.NoError(t, err)
	require.Empty(t, warnings)
}

func TestWindowsVersionMismatch(t *testing.T) {
	res := platformResult(t,
		[]string{"linux/amd64", "windows(10.0.20348.1006)/amd64"},
		[]exptypes.Platform{
			{
				ID:       "linux/amd64",
				Platform: platforms.MustParse("linux/amd64"),
			},
			{
				ID: "windows(10.0.20348.1006)/amd64",
				Platform: ocispecs.Platform{
					OS:           "windows",
					Architecture: "amd64",
					OSVersion:    "10.0.26100.33296",
				},
			},
		},
	)

	warnings, err := CheckInvalidPlatforms(t.Context(), res)
	require.NoError(t, err)
	require.Len(t, warnings, 1)
	require.Contains(t, string(warnings[0].Short), "Requested platforms")
}

func platformResult(t *testing.T, requested []string, resultPlatforms []exptypes.Platform) *result.Result[string] {
	t.Helper()
	res := &result.Result[string]{}

	attrs := map[string]string{"platform": requested[0]}
	for _, platform := range requested[1:] {
		attrs["platform"] += "," + platform
	}
	require.NoError(t, CaptureFrontendOpts(attrs, res))

	dt, err := json.Marshal(exptypes.Platforms{Platforms: resultPlatforms})
	require.NoError(t, err)
	res.AddMeta(exptypes.ExporterPlatformsKey, dt)

	for _, p := range resultPlatforms {
		res.AddRef(p.ID, p.ID)
	}
	return res
}
