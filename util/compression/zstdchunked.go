package compression

import (
	"context"
	"io"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images"
	chunkedcompressor "github.com/containers/storage/pkg/chunked/compressor"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
)

func (c zstdChunkedType) Compress(ctx context.Context, comp Config) (compressorFunc Compressor, finalize Finalizer) {
	return func(dest io.Writer, _ string) (io.WriteCloser, error) {
		level := 3
		if comp.Level != nil {
			level = *comp.Level
		}
		return chunkedcompressor.ZstdCompressor(dest, nil, &level)
	}, nil
}

func (c zstdChunkedType) Decompress(ctx context.Context, cs content.Store, desc ocispecs.Descriptor) (io.ReadCloser, error) {
	// zstd:chunked is a valid zstd stream — standard decompression works.
	return decompress(ctx, cs, desc)
}

func (c zstdChunkedType) NeedsConversion(ctx context.Context, cs content.Store, desc ocispecs.Descriptor) (bool, error) {
	if !images.IsLayerType(desc.MediaType) {
		return false, nil
	}
	ct, err := FromMediaType(desc.MediaType)
	if err != nil {
		return false, err
	}
	// zstd:chunked uses the same media type as zstd, so if the layer is
	// already zstd we skip conversion. The chunked structure is an
	// optimization; re-encoding an existing zstd layer would require
	// full decompression and recompression.
	if ct == Zstd {
		return false, nil
	}
	return true, nil
}

func (c zstdChunkedType) NeedsComputeDiffBySelf(comp Config) bool {
	return true
}

func (c zstdChunkedType) OnlySupportOCITypes() bool {
	return false
}

func (c zstdChunkedType) MediaType() string {
	// zstd:chunked uses the same OCI media type as zstd — the chunked
	// structure is transparent to registries and runtimes that don't
	// understand it. They simply decompress it as a regular zstd blob.
	return ocispecs.MediaTypeImageLayerZstd
}

func (c zstdChunkedType) String() string {
	return "zstd:chunked"
}
