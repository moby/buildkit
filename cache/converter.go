package cache

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"

	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/errdefs"
	"github.com/containerd/containerd/images"
	"github.com/containerd/containerd/images/converter"
	"github.com/containerd/containerd/images/converter/uncompress"
	"github.com/containerd/containerd/labels"
	"github.com/moby/buildkit/util/compression"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
)

// needsConversion indicates whether a conversion is needed for the specified mediatype to
// be the compressionType.
func needsConversion(mediaType string, compressionType compression.Type) (bool, error) {
	switch compressionType {
	case compression.Uncompressed:
		if !images.IsLayerType(mediaType) || uncompress.IsUncompressedType(mediaType) {
			return false, nil
		}
	case compression.Gzip:
		if !images.IsLayerType(mediaType) || isGzipCompressedType(mediaType) {
			return false, nil
		}
	case compression.EStargz:
		if !images.IsLayerType(mediaType) {
			return false, nil
		}
	default:
		return false, fmt.Errorf("unknown compression type during conversion: %q", compressionType)
	}
	return true, nil
}

// getConverter returns converter function according to the specified compression type.
// If no conversion is needed, this returns nil without error.
func getConverter(desc ocispecs.Descriptor, compressionType compression.Type) (converter.ConvertFunc, error) {
	if needs, err := needsConversion(desc.MediaType, compressionType); err != nil {
		return nil, err
	} else if !needs {
		// No conversion. No need to return an error here.
		return nil, nil
	}
	switch compressionType {
	case compression.Uncompressed:
		return uncompress.LayerConvertFunc, nil
	case compression.Gzip:
		convertFunc := func(w io.Writer) (io.WriteCloser, error) { return gzip.NewWriter(w), nil }
		return gzipLayerConvertFunc(compressionType, convertFunc, nil), nil
	case compression.EStargz:
		compressorFunc, finalize := writeEStargz()
		convertFunc := func(w io.Writer) (io.WriteCloser, error) { return compressorFunc(w, ocispecs.MediaTypeImageLayerGzip) }
		return gzipLayerConvertFunc(compressionType, convertFunc, finalize), nil
	default:
		return nil, fmt.Errorf("unknown compression type during conversion: %q", compressionType)
	}
}

func gzipLayerConvertFunc(compressionType compression.Type, convertFunc func(w io.Writer) (io.WriteCloser, error), finalize func(context.Context, content.Store) (map[string]string, error)) converter.ConvertFunc {
	return func(ctx context.Context, cs content.Store, desc ocispecs.Descriptor) (*ocispecs.Descriptor, error) {
		// prepare the source and destination
		info, err := cs.Info(ctx, desc.Digest)
		if err != nil {
			return nil, err
		}
		labelz := info.Labels
		if labelz == nil {
			labelz = make(map[string]string)
		}
		ra, err := cs.ReaderAt(ctx, desc)
		if err != nil {
			return nil, err
		}
		defer ra.Close()
		ref := fmt.Sprintf("convert-from-%s-to-%s", desc.Digest, compressionType.String())
		w, err := cs.Writer(ctx, content.WithRef(ref))
		if err != nil {
			return nil, err
		}
		defer w.Close()
		if err := w.Truncate(0); err != nil { // Old written data possibly remains
			return nil, err
		}
		zw, err := convertFunc(w)
		if err != nil {
			return nil, err
		}
		defer zw.Close()

		// convert this layer
		diffID := digest.Canonical.Digester()
		if _, err := io.Copy(zw, io.TeeReader(io.NewSectionReader(ra, 0, ra.Size()), diffID.Hash())); err != nil {
			return nil, err
		}
		if err := zw.Close(); err != nil { // Flush the writer
			return nil, err
		}
		labelz[labels.LabelUncompressed] = diffID.Digest().String() // update diffID label
		if err = w.Commit(ctx, 0, "", content.WithLabels(labelz)); err != nil && !errdefs.IsAlreadyExists(err) {
			return nil, err
		}
		if err := w.Close(); err != nil {
			return nil, err
		}
		info, err = cs.Info(ctx, w.Digest())
		if err != nil {
			return nil, err
		}

		newDesc := desc
		newDesc.MediaType = convertMediaTypeToGzip(desc.MediaType)
		newDesc.Digest = info.Digest
		newDesc.Size = info.Size
		if finalize != nil {
			a, err := finalize(ctx, cs)
			if err != nil {
				return nil, errors.Wrapf(err, "failed finalize compression")
			}
			for k, v := range a {
				if newDesc.Annotations == nil {
					newDesc.Annotations = make(map[string]string)
				}
				newDesc.Annotations[k] = v
			}
		}
		return &newDesc, nil
	}
}

func isGzipCompressedType(mt string) bool {
	switch mt {
	case
		images.MediaTypeDockerSchema2LayerGzip,
		images.MediaTypeDockerSchema2LayerForeignGzip,
		ocispecs.MediaTypeImageLayerGzip,
		ocispecs.MediaTypeImageLayerNonDistributableGzip:
		return true
	default:
		return false
	}
}

func convertMediaTypeToGzip(mt string) string {
	if uncompress.IsUncompressedType(mt) {
		if images.IsDockerType(mt) {
			mt += ".gzip"
		} else {
			mt += "+gzip"
		}
		return mt
	}
	return mt
}
