package compression

import (
	"context"
	"io"
	"maps"
	"strconv"
	"sync"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/klauspost/compress/zstd"
	"github.com/moby/buildkit/util/compression/zstdchunked"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
)

// ZstdChunkedAnnotations is the list of layer annotations describing a
// zstd:chunked blob.
var ZstdChunkedAnnotations = zstdchunked.Annotations

const zstdChunkedLabel = "buildkit.io/compression/zstd-chunked"

func (c zstdChunkedType) Compress(ctx context.Context, comp Config) (compressorFunc Compressor, finalize Finalizer) {
	var mu sync.Mutex
	var w *zstdchunked.Writer
	return func(dest io.Writer, requiredMediaType string) (io.WriteCloser, error) {
			ct, err := FromMediaType(requiredMediaType)
			if err != nil {
				return nil, err
			}
			if ct != Zstd {
				return nil, errors.Errorf("unsupported media type for zstd:chunked compressor %q", requiredMediaType)
			}
			level := zstd.SpeedDefault
			if comp.Level != nil {
				level = zstd.EncoderLevelFromZstd(*comp.Level)
			}
			zw, err := zstdchunked.NewWriter(dest, level)
			if err != nil {
				return nil, err
			}
			mu.Lock()
			w = zw
			mu.Unlock()
			return zw, nil
		}, func(ctx context.Context, cs content.Store) (map[string]string, error) {
			mu.Lock()
			w := w
			mu.Unlock()
			if w == nil {
				return nil, errors.New("cannot finalize zstd:chunked compression: no data written")
			}
			a := w.Annotations()
			if a == nil {
				return nil, errors.New("cannot finalize zstd:chunked compression: writer not closed successfully")
			}
			return maps.Clone(a), nil
		}
}

func (c zstdChunkedType) Decompress(ctx context.Context, cs content.Store, desc ocispecs.Descriptor) (io.ReadCloser, error) {
	// zstd:chunked blobs are valid zstd streams: the metadata is stored in
	// skippable frames ignored by the decoder.
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
	if ct != Zstd {
		return true, nil
	}
	if _, ok := desc.Annotations[zstdchunked.ManifestChecksumAnnotation]; ok {
		return false, nil
	}
	ok, err := c.Is(ctx, cs, desc.Digest)
	if err != nil {
		return false, err
	}
	return !ok, nil
}

func (c zstdChunkedType) NeedsComputeDiffBySelf(comp Config) bool {
	return true
}

func (c zstdChunkedType) OnlySupportOCITypes() bool {
	// Consumers rely on the layer annotations to locate the TOC, and
	// Docker schema2 manifests can't be relied on to carry them.
	return true
}

func (c zstdChunkedType) MediaType() string {
	// zstd:chunked uses the same media type as zstd so that it can be
	// consumed as a regular zstd layer by runtimes that don't support it.
	return ocispecs.MediaTypeImageLayerZstd
}

func (c zstdChunkedType) String() string {
	return "zstd:chunked"
}

// Is returns true when the specified digest of content exists in the content
// store and it's a zstd:chunked blob.
func (c zstdChunkedType) Is(ctx context.Context, cs content.Store, dgst digest.Digest) (bool, error) {
	info, err := cs.Info(ctx, dgst)
	if err != nil {
		return false, nil
	}
	if v, ok := info.Labels[zstdChunkedLabel]; ok {
		if b, err := strconv.ParseBool(v); err == nil {
			return b, nil
		}
	}

	res := func() bool {
		ra, err := cs.ReaderAt(ctx, ocispecs.Descriptor{Digest: dgst})
		if err != nil {
			return false
		}
		defer ra.Close()
		return zstdchunked.HasFooter(ra, ra.Size())
	}()

	if info.Labels == nil {
		info.Labels = make(map[string]string)
	}
	info.Labels[zstdChunkedLabel] = strconv.FormatBool(res) // cache the result
	if _, err := cs.Update(ctx, info, "labels."+zstdChunkedLabel); err != nil {
		return false, err
	}
	return res, nil
}
