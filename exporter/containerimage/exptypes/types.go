package exptypes

import (
	"context"

	"github.com/moby/buildkit/solver/result"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
)

const (
	ExporterConfigDigestKey      = "config.digest"
	ExporterImageNameKey         = "image.name"
	ExporterImageDigestKey       = "containerimage.digest"
	ExporterImageConfigKey       = "containerimage.config"
	ExporterImageConfigDigestKey = "containerimage.config.digest"
	ExporterImageDescriptorKey   = "containerimage.descriptor"
	ExporterImageBaseConfigKey   = "containerimage.base.config"
	ExporterPlatformsKey         = "refs.platforms"

	ExporterArtifactKey           = "containerimage.artifact"
	ExporterArtifactTypeKey       = "containerimage.artifact.type"
	ExporterArtifactConfigTypeKey = "containerimage.artifact.config.mediatype"
	ExporterArtifactLayersKey     = "containerimage.artifact.layers"
	ExporterPostprocessorsKey     = "containerimage.postprocessors"
	ExporterOCILayoutKey          = "containerimage.oci-layout"

	PostprocessInputKey = "input"
)

// KnownRefMetadataKeys are the subset of exporter keys that can be suffixed by
// a platform to become platform specific
var KnownRefMetadataKeys = []string{
	ExporterImageConfigKey,
	ExporterImageBaseConfigKey,
}

type Platforms struct {
	Platforms []Platform
}

type Platform struct {
	ID       string
	Platform ocispecs.Platform
}

type InlineCacheEntry struct {
	Data []byte
}
type InlineCache func(ctx context.Context) (*result.Result[*InlineCacheEntry], error)

type PostprocessRequest struct {
	Frontend    string            `json:"frontend"`
	FrontendOpt map[string]string `json:"frontendOpt,omitempty"`
}
