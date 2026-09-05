package compression

import (
	"testing"

	"github.com/containerd/containerd/v2/core/images"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

func TestIsExactMediaType(t *testing.T) {
	for _, tc := range []struct {
		mediaType string
		exact     Type // the one type the media type identifies, or nil
	}{
		{ocispecs.MediaTypeImageLayer, Uncompressed},
		{images.MediaTypeDockerSchema2Layer, Uncompressed},
		{ocispecs.MediaTypeImageLayerNonDistributable, Uncompressed}, //nolint:staticcheck // ignore SA1019: Non-distributable layers are deprecated, and not recommended for future use.
		{images.MediaTypeDockerSchema2LayerForeign, Uncompressed},
		{ocispecs.MediaTypeImageLayerGzip, Gzip},
		{images.MediaTypeDockerSchema2LayerGzip, Gzip},
		{ocispecs.MediaTypeImageLayerNonDistributableGzip, Gzip}, //nolint:staticcheck // ignore SA1019: Non-distributable layers are deprecated, and not recommended for future use.
		{images.MediaTypeDockerSchema2LayerForeignGzip, Gzip},
		{ocispecs.MediaTypeImageLayerZstd, Zstd},
		{images.MediaTypeDockerSchema2LayerZstd, Zstd},
		{ocispecs.MediaTypeImageLayerNonDistributableZstd, Zstd}, //nolint:staticcheck // ignore SA1019: Non-distributable layers are deprecated, and not recommended for future use.
		{"application/octet-stream", nil},
		{"", nil},
	} {
		t.Run(tc.mediaType, func(t *testing.T) {
			// EStargz is never identified by a media type: it shares gzip's.
			for _, ct := range []Type{Uncompressed, Gzip, EStargz, Zstd} {
				require.Equal(t, ct == tc.exact, IsExactMediaType(ct, tc.mediaType), "IsExactMediaType(%s, %q)", ct, tc.mediaType)
			}
		})
	}
}

// TestIsMediaType pins the overlap that makes IsExactMediaType necessary:
// IsMediaType compares against what a type would emit, and eStargz emits the
// gzip media type, so it cannot tell the two apart.
func TestIsMediaType(t *testing.T) {
	require.True(t, IsMediaType(Gzip, ocispecs.MediaTypeImageLayerGzip))
	require.True(t, IsMediaType(Gzip, images.MediaTypeDockerSchema2LayerGzip))
	require.True(t, IsMediaType(EStargz, ocispecs.MediaTypeImageLayerGzip))
	require.True(t, IsMediaType(Zstd, ocispecs.MediaTypeImageLayerZstd))
	require.True(t, IsMediaType(Uncompressed, ocispecs.MediaTypeImageLayer))

	require.False(t, IsMediaType(Zstd, ocispecs.MediaTypeImageLayerGzip))
	require.False(t, IsMediaType(Gzip, "application/octet-stream"))
	require.False(t, IsMediaType(Gzip, ""))
}
