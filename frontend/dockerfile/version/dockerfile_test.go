package version

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeDockerfileVersion(t *testing.T) {
	for _, tc := range []struct {
		name            string
		dockerfileVer   string
		buildkitVersion string
		want            string
		wantErr         bool
	}{
		{name: "stable short", dockerfileVer: "1.24", buildkitVersion: "v0.29.0", want: "1.24.0"},
		{name: "stable patch", dockerfileVer: "1.24", buildkitVersion: "v0.29.1", want: "1.24.0"},
		{name: "prerelease", dockerfileVer: "1.24", buildkitVersion: "v0.29.0-rc1", want: "1.24.0-rc1"},
		{name: "describe", dockerfileVer: "1.24", buildkitVersion: "v0.29.0-45-g95735c4ef", want: "1.24.0-45-g95735c4ef"},
		{name: "metadata", dockerfileVer: "1.24", buildkitVersion: "v0.29.0+unknown", want: "1.24.0-dev"},
		{name: "invalid buildkit", dockerfileVer: "1.24", buildkitVersion: "not-semver", want: "1.24.0-dev"},
		{name: "dockerfile with patch", dockerfileVer: "1.24.3", buildkitVersion: "v0.29.0", want: "1.24.3"},
		{name: "invalid dockerfile major only", dockerfileVer: "1", buildkitVersion: "v0.29.0", wantErr: true},
		{name: "invalid dockerfile suffix", dockerfileVer: "1.24.x", buildkitVersion: "v0.29.0", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeDockerfileVersion(tc.dockerfileVer, tc.buildkitVersion)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}
