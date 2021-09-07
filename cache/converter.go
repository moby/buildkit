package cache

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"

	cdcompression "github.com/containerd/containerd/archive/compression"
	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/errdefs"
	"github.com/containerd/containerd/images"
	"github.com/containerd/containerd/images/converter"
	"github.com/containerd/containerd/labels"
	"github.com/klauspost/compress/zstd"
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
		if !images.IsLayerType(mediaType) || compression.FromMediaType(mediaType) == compression.Uncompressed {
			return false, nil
		}
	case compression.Gzip:
		if !images.IsLayerType(mediaType) || compression.FromMediaType(mediaType) == compression.Gzip {
			return false, nil
		}
	case compression.Zstd:
		if !images.IsLayerType(mediaType) || compression.FromMediaType(mediaType) == compression.Zstd {
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

	c := conversion{target: compressionType}

	from := compression.FromMediaType(desc.MediaType)
	switch from {
	case compression.Uncompressed:
	case compression.Gzip, compression.Zstd:
		c.decompress = cdcompression.DecompressStream
	default:
		return nil, errors.Errorf("unsupported source compression type %q from mediatype %q", from, desc.MediaType)
	}

	switch compressionType {
	case compression.Uncompressed:
	case compression.Gzip:
		c.compress = func(w io.Writer) (io.WriteCloser, error) {
			return gzip.NewWriter(w), nil
		}
	case compression.Zstd:
		c.compress = func(w io.Writer) (io.WriteCloser, error) {
			return zstd.NewWriter(w)
		}
	case compression.EStargz:
		compressorFunc, finalize := writeEStargz()
		c.compress = func(w io.Writer) (io.WriteCloser, error) {
			return compressorFunc(w, ocispecs.MediaTypeImageLayerGzip)
		}
		c.finalize = finalize
	default:
		return nil, errors.Errorf("unknown target compression type during conversion: %q", compressionType)
	}

	return (&c).convert, nil
}

type conversion struct {
	target     compression.Type
	decompress func(io.Reader) (cdcompression.DecompressReadCloser, error)
	compress   func(w io.Writer) (io.WriteCloser, error)
	finalize   func(context.Context, content.Store) (map[string]string, error)
}

func (c *conversion) convert(ctx context.Context, cs content.Store, desc ocispecs.Descriptor) (*ocispecs.Descriptor, error) {
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
	ref := fmt.Sprintf("convert-from-%s-to-%s", desc.Digest, c.target.String())
	w, err := cs.Writer(ctx, content.WithRef(ref))
	if err != nil {
		return nil, err
	}
	defer w.Close()
	if err := w.Truncate(0); err != nil { // Old written data possibly remains
		return nil, err
	}
	var zw io.WriteCloser = w
	var compress io.WriteCloser
	if c.compress != nil {
		zw, err = c.compress(zw)
		if err != nil {
			return nil, err
		}
		defer zw.Close()
		compress = zw
	}

	// convert this layer
	diffID := digest.Canonical.Digester()
	var rdr io.Reader = io.NewSectionReader(ra, 0, ra.Size())
	if c.decompress != nil {
		rc, err := c.decompress(rdr)
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		rdr = rc
	}
	if _, err := io.Copy(zw, io.TeeReader(rdr, diffID.Hash())); err != nil {
		return nil, err
	}
	if compress != nil {
		if err := compress.Close(); err != nil { // Flush the writer
			return nil, err
		}
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
	newDesc.MediaType = c.target.DefaultMediaType()
	newDesc.Digest = info.Digest
	newDesc.Size = info.Size
	if c.finalize != nil {
		a, err := c.finalize(ctx, cs)
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
