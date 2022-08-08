package util

import (
	"testing"

	controlapi "github.com/moby/buildkit/api/services/control"
	"github.com/stretchr/testify/require"
)

func TestDedupCacheOptions(t *testing.T) {
	var testCases = []struct {
		name     string
		opts     []*controlapi.CacheOptionsEntry
		expected []*controlapi.CacheOptionsEntry
	}{
		{
			name: "deduplicates opts",
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
				{
					Type: "azblob",
					Attrs: map[string]string{
						"account-url":  "url",
						"blobs_prefix": "prefix",
						"name":         "name",
					},
				},
				{
					Type: "azblob",
					Attrs: map[string]string{
						"account-url":  "url",
						"blobs_prefix": "prefix",
						"name":         "name",
					},
				},
			},
			expected: []*controlapi.CacheOptionsEntry{
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
					Type: "azblob",
					Attrs: map[string]string{
						"account-url":  "url",
						"blobs_prefix": "prefix",
						"name":         "name",
					},
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			opts, err := DedupCacheOptions(tc.opts)
			require.NoError(t, err)
			require.ElementsMatch(t, opts, tc.expected)
		})
	}
}
