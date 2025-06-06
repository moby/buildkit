package attestations

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParse(t *testing.T) {
	for _, tc := range []struct {
		name     string
		values   map[string]string
		expected map[string]map[string]string
	}{
		{
			name: "simple",
			values: map[string]string{
				"attest:sbom":       "generator=docker.io/foo/bar",
				"attest:provenance": "mode=max",
			},
			expected: map[string]map[string]string{
				"sbom": {
					"generator": "docker.io/foo/bar",
				},
				"provenance": {
					"mode":    "max",
					"version": "v0.2", // intentionally not const
				},
			},
		},
		{
			name: "extra params",
			values: map[string]string{
				"attest:sbom": "generator=docker.io/foo/bar,param1=foo,param2=bar",
			},
			expected: map[string]map[string]string{
				"sbom": {
					"generator": "docker.io/foo/bar",
					"param1":    "foo",
					"param2":    "bar",
				},
			},
		},
		{
			name: "extra params (complex)",
			values: map[string]string{
				"attest:sbom": "\"generator=docker.io/foo/bar\",\"param1=foo\",\"param2=bar\",\"param3=abc,def\"",
			},
			expected: map[string]map[string]string{
				"sbom": {
					"generator": "docker.io/foo/bar",
					"param1":    "foo",
					"param2":    "bar",
					"param3":    "abc,def",
				},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			attests, err := Parse(tc.values)
			require.NoError(t, err)
			_ = attests
			assert.Equal(t, tc.expected, attests)
		})
	}
}
