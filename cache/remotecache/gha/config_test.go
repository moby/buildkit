package gha

import (
	"maps"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/moby/buildkit/util/compression"
	"github.com/stretchr/testify/require"
)

// TestExporterCompressionAttributes checks that the compression attributes
// passed to --export-cache are reflected in the exporter config instead of
// always falling back to the default compression, and that invalid values are
// rejected when the exporter is resolved.
func TestExporterCompressionAttributes(t *testing.T) {
	tests := []struct {
		name    string
		attrs   map[string]string
		want    compression.Config
		wantErr string
	}{
		{
			name:  "default",
			attrs: map[string]string{},
			want:  compression.New(compression.Default),
		},
		{
			name:  "zstd",
			attrs: map[string]string{"compression": "zstd"},
			want:  compression.New(compression.Zstd),
		},
		{
			name:  "level",
			attrs: map[string]string{"compression": "zstd", "compression-level": "1"},
			want:  compression.New(compression.Zstd).SetLevel(1),
		},
		{
			name:  "force",
			attrs: map[string]string{"compression": "zstd", "force-compression": "true"},
			want:  compression.New(compression.Zstd).SetForce(true),
		},
		{
			name:    "unknown compression type",
			attrs:   map[string]string{"compression": "lzma"},
			wantErr: "unsupported compression type lzma",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exp, err := ResolveCacheExporterFunc(nil, nil)(t.Context(), nil, testAttrs(t, tt.attrs))
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, exp.Config().Compression)
		})
	}

	// Compression only applies to exports: importing ignores the attributes.
	_, _, err := ResolveCacheImporterFunc(nil, nil)(t.Context(), nil, testAttrs(t, map[string]string{"compression": "lzma"}))
	require.NoError(t, err)
}

// testAttrs adds a valid runtime token and cache URL to attrs: resolving
// parses the token's claims but doesn't contact the cache.
func testAttrs(t *testing.T, attrs map[string]string) map[string]string {
	t.Helper()
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"ac":  "[]",
		"nbf": time.Now().Add(-time.Minute).Unix(),
		"exp": time.Now().Add(time.Hour).Unix(),
	}).SignedString([]byte("test"))
	require.NoError(t, err)
	all := map[string]string{attrToken: token, attrURL: "http://cache.invalid/"}
	maps.Copy(all, attrs)
	return all
}
