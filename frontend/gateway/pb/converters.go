package moby_buildkit_v1_frontend //nolint:revive,staticcheck

import (
	"maps"

	"github.com/moby/buildkit/util/compression"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
)

func DescriptorFromPB(pb *Descriptor) ocispecs.Descriptor {
	if pb == nil {
		return ocispecs.Descriptor{}
	}
	return ocispecs.Descriptor{
		MediaType:   pb.MediaType,
		Size:        pb.Size,
		Digest:      digest.Digest(pb.Digest),
		Annotations: maps.Clone(pb.Annotations),
	}
}

func DescriptorToPB(desc ocispecs.Descriptor) *Descriptor {
	return &Descriptor{
		MediaType:   desc.MediaType,
		Size:        desc.Size,
		Digest:      desc.Digest.String(),
		Annotations: maps.Clone(desc.Annotations),
	}
}

func CompressionFromPB(pb *Compression) compression.Config {
	if pb == nil {
		return compression.New(compression.Default)
	}

	tp := compressTypeFromPB(pb.Type)
	if tp == nil {
		tp = compression.Default
	}
	cfg := compression.New(tp)
	if pb.Force {
		cfg = cfg.SetForce(true)
	}
	if pb.HasLevel {
		cfg = cfg.SetLevel(int(pb.Level))
	}
	return cfg
}

func compressTypeFromPB(t Compression_Type) compression.Type {
	switch t {
	case Compression_UNCOMPRESSED:
		return compression.Uncompressed
	case Compression_GZIP:
		return compression.Gzip
	case Compression_ESTARGZ:
		return compression.EStargz
	case Compression_ZSTD:
		return compression.Zstd
	default:
		return nil
	}
}

func CompressionToPB(cfg compression.Config) *Compression {
	t := compressTypeToPB(cfg.Type)
	pb := &Compression{
		Type:  t,
		Force: cfg.Force,
	}
	if cfg.Level != nil {
		pb.HasLevel = true
		pb.Level = int32(*cfg.Level)
	}
	return pb
}

func compressTypeToPB(t compression.Type) Compression_Type {
	switch t {
	case compression.Uncompressed:
		return Compression_UNCOMPRESSED
	case compression.Gzip:
		return Compression_GZIP
	case compression.EStargz:
		return Compression_ESTARGZ
	case compression.Zstd:
		return Compression_ZSTD
	default:
		return Compression_UNKNOWN
	}
}
