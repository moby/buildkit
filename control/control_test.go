package control

import (
	"testing"

	controlapi "github.com/moby/buildkit/api/services/control"
	"github.com/stretchr/testify/require"
)

func TestDuplicateCacheOptions(t *testing.T) {
	var testCases = []struct {
		name     string
		opts     []*controlapi.CacheOptionsEntry
		expected []uint
	}{
		{
			name: "avoids unique opts",
			opts: []*controlapi.CacheOptionsEntry{
				{
					Type: "registry",
					Attrs: map[string]string{
						"ref": "example.com/ref:v1.0.0",
					},
				},
				{
					Type: "local",
					Attrs: map[string]string{
						"dest": "/path/for/export",
					},
				},
			},
			expected: nil,
		},
		{
			name: "finds duplicate opts",
			opts: []*controlapi.CacheOptionsEntry{
				{
					Type: "registry",
					Attrs: map[string]string{
						"ref": "example.com/ref:v1.0.0",
					},
				},
				{
					Type: "registry",
					Attrs: map[string]string{
						"ref": "example.com/ref:v1.0.0",
					},
				},
				{
					Type: "local",
					Attrs: map[string]string{
						"dest": "/path/for/export",
					},
				},
				{
					Type: "local",
					Attrs: map[string]string{
						"dest": "/path/for/export",
					},
				},
			},
			expected: []uint{0, 2},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := findDuplicateCacheOptions(tc.opts)
			require.NoError(t, err)
			for i, j := range tc.expected {
				require.Equal(t, p[i], tc.opts[j])
			}
		})
	}
}
