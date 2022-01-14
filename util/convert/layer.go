package convert

import (
	"github.com/containerd/containerd/images"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
)

// LayertoDistributable changes the passed in media type to the "distributable" version of the media type.
func LayerToDistributable(oci bool, mt string) string {
	if !images.IsNonDistributable(mt) {
		// Layer is already a distributable media type (or this is not even a layer).
		// No conversion needed
		return mt
	}

	if oci {
		switch mt {
		case ocispecs.MediaTypeImageLayerNonDistributable:
			return ocispecs.MediaTypeImageLayer
		case ocispecs.MediaTypeImageLayerNonDistributableGzip:
			return ocispecs.MediaTypeImageLayerGzip
		case ocispecs.MediaTypeImageLayerNonDistributableZstd:
			return ocispecs.MediaTypeImageLayerZstd
		default:
			return mt
		}
	}

	switch mt {
	case images.MediaTypeDockerSchema2LayerForeign:
		return images.MediaTypeDockerSchema2Layer
	case images.MediaTypeDockerSchema2LayerForeignGzip:
		return images.MediaTypeDockerSchema2LayerGzip
	default:
		return mt
	}
}
