package gha

import (
	"maps"
	"testing"

	"github.com/moby/buildkit/util/compression"
	"github.com/stretchr/testify/require"
)

// TestExporterCompressionAttributes checks that the compression attributes
// passed to --export-cache are reflected in the exporter config instead of
// always falling back to the default compression, and that invalid values are
// rejected.
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
			attrs := map[string]string{attrToken: "token", attrURL: "http://cache.invalid/"}
			maps.Copy(attrs, tt.attrs)
			cfg, err := getConfig(nil, nil, attrs)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, (&exporter{config: cfg}).Config().Compression)
		})
	}
}
