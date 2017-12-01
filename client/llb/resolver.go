package llb

import (
	digest "github.com/opencontainers/go-digest"
	"golang.org/x/net/context"
)

func WithMetaResolver(mr ImageMetaResolver) ImageOption {
	return func(ii *ImageInfo) {
		ii.metaResolver = mr
	}
}

type ImageMetaResolver interface {
	ResolveImageConfig(ctx context.Context, ref string) (digest.Digest, []byte, error)
}
