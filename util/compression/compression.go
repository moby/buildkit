package compression

import (
	"bytes"
	"context"
	"io"

	cdcompression "github.com/containerd/containerd/archive/compression"
	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/images"
	"github.com/containerd/stargz-snapshotter/estargz"
	"github.com/moby/buildkit/util/bklog"
	"github.com/moby/buildkit/util/iohelper"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
)

type Compressor func(dest io.Writer, mediaType string) (io.WriteCloser, error)
type Decompressor func(ctx context.Context, cs content.Store, desc ocispecs.Descriptor) (io.ReadCloser, error)
type Finalizer func(context.Context, content.Store) (map[string]string, error)

// Type represents compression type for blob data, which needs
// to be implemented for each compression type.
type Type interface {
	Compress(ctx context.Context, comp Config) (compressorFunc Compressor, finalize Finalizer)
	Decompress(ctx context.Context, cs content.Store, desc ocispecs.Descriptor) (io.ReadCloser, error)
	NeedsConversion(ctx context.Context, cs content.Store, desc ocispecs.Descriptor) (bool, error)
	NeedsComputeDiffBySelf(comp Config) bool
	OnlySupportOCITypes() bool
	MediaType() string
	String() string
}

var (
	// Uncompressed indicates no compression.
	Uncompressed = uncompressedType{}

	// Gzip is used for blob data.
	Gzip = gzipType{}

	// EStargz is used for estargz data.
	EStargz = estargzType{}

	// Zstd is used for Zstandard data.
	Zstd = zstdType{}
)

var toCompressionType = map[string]Type{
	Uncompressed.String(): Uncompressed,
	Gzip.String():         Gzip,
	EStargz.String():      EStargz,
	Zstd.String():         Zstd,
}

// mediaTypeToCompressionType maps media-types to compression types.
var mediaTypeToCompressionType = map[string]Type{
	ocispecs.MediaTypeImageLayer:                     Uncompressed,
	ocispecs.MediaTypeImageLayerNonDistributable:     Uncompressed, //nolint:staticcheck // ignore SA1019: Non-distributable layers are deprecated, and not recommended for future use.
	ocispecs.MediaTypeImageLayerGzip:                 Gzip,
	ocispecs.MediaTypeImageLayerNonDistributableGzip: Gzip, //nolint:staticcheck // ignore SA1019: Non-distributable layers are deprecated, and not recommended for future use.
	ocispecs.MediaTypeImageLayerZstd:                 Zstd,
	ocispecs.MediaTypeImageLayerNonDistributableZstd: Zstd, //nolint:staticcheck // ignore SA1019: Non-distributable layers are deprecated, and not recommended for future use.
}

type Config struct {
	Type  Type
	Force bool
	Level *int
}

func New(t Type) Config {
	return Config{
		Type: t,
	}
}

func (c Config) SetForce(v bool) Config {
	c.Force = v
	return c
}

func (c Config) SetLevel(l int) Config {
	c.Level = &l
	return c
}

const (
	mediaTypeDockerSchema2LayerZstd = images.MediaTypeDockerSchema2Layer + ".zstd"
)

var Default = Gzip

// Parse returns the [Type] registered for the given name. It returns an error
// if no compression-type is registered for the given name.
func Parse(name string) (Type, error) {
	ct, ok := toCompressionType[name]
	if !ok {
		return nil, errors.Errorf("unsupported compression type %s", name)
	}
	return ct, nil
}

// FromMediaType returns the [Type] registered for the given mediaType.
// It returns an error if the given mediaType is not supported or no
// compression-type is registered for the given type.
func FromMediaType(mediaType string) (Type, error) {
	mt, ok := toOCILayerType[mediaType]
	if !ok {
		return nil, errors.Errorf("unsupported media type %s", mediaType)
	}
	ct, ok := mediaTypeToCompressionType[mt]
	if !ok {
		return nil, errors.Errorf("unsupported media type %s", mediaType)
	}
	return ct, nil
}

func IsMediaType(ct Type, mt string) bool {
	mt, ok := toOCILayerType[mt]
	if !ok {
		return false
	}
	return mt == ct.MediaType()
}

// DetectLayerMediaType returns media type from existing blob data.
func DetectLayerMediaType(ctx context.Context, cs content.Store, id digest.Digest, oci bool) (string, error) {
	ra, err := cs.ReaderAt(ctx, ocispecs.Descriptor{Digest: id})
	if err != nil {
		return "", err
	}
	defer ra.Close()

	ct, err := detectCompressionType(io.NewSectionReader(ra, 0, ra.Size()))
	if err != nil {
		return "", err
	}

	switch ct {
	case Uncompressed:
		if oci {
			return ocispecs.MediaTypeImageLayer, nil
		}
		return images.MediaTypeDockerSchema2Layer, nil
	case Gzip, EStargz:
		if oci {
			return ocispecs.MediaTypeImageLayerGzip, nil
		}
		return images.MediaTypeDockerSchema2LayerGzip, nil

	default:
		return "", errors.Errorf("failed to detect layer %v compression type", id)
	}
}

// detectCompressionType detects compression type from real blob data.
func detectCompressionType(cr *io.SectionReader) (Type, error) {
	var buf [10]byte
	var n int
	var err error

	if n, err = cr.Read(buf[:]); err != nil && err != io.EOF {
		// Note: we'll ignore any io.EOF error because there are some
		// odd cases where the layer.tar file will be empty (zero bytes)
		// and we'll just treat it as a non-compressed stream and that
		// means just create an empty layer.
		//
		// See issue docker/docker#18170
		return nil, err
	}

	if _, _, err := estargz.OpenFooter(cr); err == nil {
		return EStargz, nil
	}

	for c, m := range map[Type][]byte{
		Gzip: {0x1F, 0x8B, 0x08},
		Zstd: {0x28, 0xB5, 0x2F, 0xFD},
	} {
		if n < len(m) {
			continue
		}
		if bytes.Equal(m, buf[:len(m)]) {
			return c, nil
		}
	}

	return Uncompressed, nil
}

var toDockerLayerType = map[string]string{
	ocispecs.MediaTypeImageLayer:                     images.MediaTypeDockerSchema2Layer,
	images.MediaTypeDockerSchema2Layer:               images.MediaTypeDockerSchema2Layer,
	ocispecs.MediaTypeImageLayerGzip:                 images.MediaTypeDockerSchema2LayerGzip,
	images.MediaTypeDockerSchema2LayerGzip:           images.MediaTypeDockerSchema2LayerGzip,
	images.MediaTypeDockerSchema2LayerForeign:        images.MediaTypeDockerSchema2LayerForeign,
	images.MediaTypeDockerSchema2LayerForeignGzip:    images.MediaTypeDockerSchema2LayerForeignGzip,
	ocispecs.MediaTypeImageLayerNonDistributable:     images.MediaTypeDockerSchema2LayerForeign,     //nolint:staticcheck // ignore SA1019: Non-distributable layers are deprecated, and not recommended for future use.
	ocispecs.MediaTypeImageLayerNonDistributableGzip: images.MediaTypeDockerSchema2LayerForeignGzip, //nolint:staticcheck // ignore SA1019: Non-distributable layers are deprecated, and not recommended for future use.
	ocispecs.MediaTypeImageLayerZstd:                 mediaTypeDockerSchema2LayerZstd,
	mediaTypeDockerSchema2LayerZstd:                  mediaTypeDockerSchema2LayerZstd,
}

var toOCILayerType = map[string]string{
	ocispecs.MediaTypeImageLayer:                     ocispecs.MediaTypeImageLayer,
	ocispecs.MediaTypeImageLayerNonDistributable:     ocispecs.MediaTypeImageLayerNonDistributable,     //nolint:staticcheck // ignore SA1019: Non-distributable layers are deprecated, and not recommended for future use.
	ocispecs.MediaTypeImageLayerNonDistributableGzip: ocispecs.MediaTypeImageLayerNonDistributableGzip, //nolint:staticcheck // ignore SA1019: Non-distributable layers are deprecated, and not recommended for future use.
	ocispecs.MediaTypeImageLayerNonDistributableZstd: ocispecs.MediaTypeImageLayerNonDistributableZstd, //nolint:staticcheck // ignore SA1019: Non-distributable layers are deprecated, and not recommended for future use.
	images.MediaTypeDockerSchema2Layer:               ocispecs.MediaTypeImageLayer,
	ocispecs.MediaTypeImageLayerGzip:                 ocispecs.MediaTypeImageLayerGzip,
	images.MediaTypeDockerSchema2LayerGzip:           ocispecs.MediaTypeImageLayerGzip,
	images.MediaTypeDockerSchema2LayerForeign:        ocispecs.MediaTypeImageLayerNonDistributable,     //nolint:staticcheck // ignore SA1019: Non-distributable layers are deprecated, and not recommended for future use.
	images.MediaTypeDockerSchema2LayerForeignGzip:    ocispecs.MediaTypeImageLayerNonDistributableGzip, //nolint:staticcheck // ignore SA1019: Non-distributable layers are deprecated, and not recommended for future use.
	ocispecs.MediaTypeImageLayerZstd:                 ocispecs.MediaTypeImageLayerZstd,
	mediaTypeDockerSchema2LayerZstd:                  ocispecs.MediaTypeImageLayerZstd,
}

func convertLayerMediaType(ctx context.Context, mediaType string, oci bool) string {
	var converted string
	if oci {
		converted = toOCILayerType[mediaType]
	} else {
		converted = toDockerLayerType[mediaType]
	}
	if converted == "" {
		bklog.G(ctx).Warnf("unhandled conversion for mediatype %q", mediaType)
		return mediaType
	}
	return converted
}

func ConvertAllLayerMediaTypes(ctx context.Context, oci bool, descs ...ocispecs.Descriptor) []ocispecs.Descriptor {
	var converted []ocispecs.Descriptor
	for _, desc := range descs {
		desc.MediaType = convertLayerMediaType(ctx, desc.MediaType, oci)
		converted = append(converted, desc)
	}
	return converted
}

func decompress(ctx context.Context, cs content.Store, desc ocispecs.Descriptor) (r io.ReadCloser, err error) {
	ra, err := cs.ReaderAt(ctx, desc)
	if err != nil {
		return nil, err
	}
	esgz, err := EStargz.Is(ctx, cs, desc.Digest)
	if err != nil {
		return nil, err
	} else if esgz {
		r, err = decompressEStargz(io.NewSectionReader(ra, 0, ra.Size()))
		if err != nil {
			return nil, err
		}
	} else {
		r, err = cdcompression.DecompressStream(io.NewSectionReader(ra, 0, ra.Size()))
		if err != nil {
			return nil, err
		}
	}
	return iohelper.WithCloser(r, ra.Close), nil
}
