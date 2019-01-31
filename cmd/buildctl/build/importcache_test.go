package build

import (
	"testing"

	"github.com/moby/buildkit/client"
	"github.com/stretchr/testify/require"
)

func TestParseImportCache(t *testing.T) {
	type testCase struct {
		importCaches []string // --import-cache
		expected     []client.CacheOptionsEntry
		expectedErr  string
	}
	testCases := []testCase{
		{
			importCaches: []string{"type=registry,ref=example.com/foo/bar", "type=local,src=/path/to/store"},
			expected: []client.CacheOptionsEntry{
				{
					Type: "registry",
					Attrs: map[string]string{
						"ref": "example.com/foo/bar",
					},
				},
				{
					Type: "local",
					Attrs: map[string]string{
						"src": "/path/to/store",
					},
				},
			},
		},
		{
			importCaches: []string{"example.com/foo/bar", "example.com/baz/qux"},
			expected: []client.CacheOptionsEntry{
				{
					Type: "registry",
					Attrs: map[string]string{
						"ref": "example.com/foo/bar",
					},
				},
				{
					Type: "registry",
					Attrs: map[string]string{
						"ref": "example.com/baz/qux",
					},
				},
			},
		},
	}
	for _, tc := range testCases {
		im, err := ParseImportCache(tc.importCaches)
		if tc.expectedErr == "" {
			require.EqualValues(t, tc.expected, im)
		} else {
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.expectedErr)
		}
	}
}
