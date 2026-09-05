package s3

import (
	"maps"
	"path/filepath"
	"testing"

	"github.com/moby/buildkit/cache/remotecache"
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
			name:  "gzip",
			attrs: map[string]string{"compression": "gzip"},
			want:  compression.New(compression.Gzip),
		},
		{
			name:  "zstd",
			attrs: map[string]string{"compression": "zstd"},
			want:  compression.New(compression.Zstd),
		},
		{
			name:  "uncompressed",
			attrs: map[string]string{"compression": "uncompressed"},
			want:  compression.New(compression.Uncompressed),
		},
		{
			name:  "estargz",
			attrs: map[string]string{"compression": "estargz"},
			want:  compression.New(compression.EStargz),
		},
		{
			name:  "level",
			attrs: map[string]string{"compression": "zstd", "compression-level": "12"},
			want:  compression.New(compression.Zstd).SetLevel(12),
		},
		{
			name:  "force",
			attrs: map[string]string{"compression": "zstd", "force-compression": "true"},
			want:  compression.New(compression.Zstd).SetForce(true),
		},
		{
			name:  "force without value",
			attrs: map[string]string{"force-compression": ""},
			want:  compression.New(compression.Default).SetForce(true),
		},
		{
			name:    "unknown compression type",
			attrs:   map[string]string{"compression": "lzma"},
			wantErr: "unsupported compression type lzma",
		},
		{
			name:    "non-integer compression level",
			attrs:   map[string]string{"compression-level": "fastest"},
			wantErr: "non-integer value fastest specified for compression-level",
		},
		{
			name:    "non-bool force compression",
			attrs:   map[string]string{"force-compression": "yes please"},
			wantErr: "non-bool value yes please specified for force-compression",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exp, err := resolveTestExporter(t, tt.attrs)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, exp.Config().Compression)
		})
	}
}

// resolveTestExporter resolves the s3 exporter for attrs on top of the
// minimal required ones. Resolving builds a real AWS SDK client whose config
// loader reads the ambient AWS environment, so point it at nothing: otherwise
// an AWS_PROFILE or ~/.aws/config on the developer's machine can fail the
// test for reasons unrelated to the attributes.
func resolveTestExporter(t *testing.T, attrs map[string]string) (remotecache.Exporter, error) {
	t.Helper()
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(t.TempDir(), "config"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(t.TempDir(), "credentials"))

	all := map[string]string{
		attrBucket: "bucket",
		attrRegion: "us-east-1",
	}
	maps.Copy(all, attrs)
	return ResolveCacheExporterFunc()(t.Context(), nil, all)
}
