package compression

import (
	"context"
	"io"

	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/images"
	"github.com/docker/docker/pkg/ioutils"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
)

type nopWriteCloser struct {
	io.Writer
}

func (w *nopWriteCloser) Close() error {
	return nil
}

func (c uncompressedType) Compress(comp Config) (compressorFunc Compressor, finalize Finalizer) {
	return func(dest io.Writer, mediaType string) (io.WriteCloser, error) {
		return &nopWriteCloser{dest}, nil
	}, nil
}

func (c uncompressedType) Decompress(ctx context.Context, cs content.Store, desc ocispecs.Descriptor) (io.ReadCloser, error) {
	ra, err := cs.ReaderAt(ctx, desc)
	if err != nil {
		return nil, err
	}
	rdr := io.NewSectionReader(ra, 0, ra.Size())
	return ioutils.NewReadCloserWrapper(rdr, ra.Close), nil
}

func (c uncompressedType) NeedsConversion(ctx context.Context, cs content.Store, desc ocispecs.Descriptor) (bool, error) {
	if !images.IsLayerType(desc.MediaType) {
		return false, nil
	}
	ct, err := FromMediaType(desc.MediaType)
	if err != nil {
		return false, err
	}
	if ct == Uncompressed {
		return false, nil
	}
	return true, nil
}

func (c uncompressedType) NeedsComputeDiffBySelf() bool {
	return false
}

func (c uncompressedType) OnlySupportOCITypes() bool {
	return false
}

func (c uncompressedType) MediaType() string {
	return ocispecs.MediaTypeImageLayer
}

func (c uncompressedType) String() string {
	return "uncompressed"
}
