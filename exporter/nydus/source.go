package nydus

import (
	"context"

	"github.com/containerd/containerd/mount"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"

	"github.com/dragonflyoss/image-service/contrib/nydusify/pkg/converter/provider"

	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/session"
)

type sourceLayer struct {
	ref       cache.ImmutableRef
	sessionID string
	size      int64
}

type sourceProvider struct {
	ref       cache.ImmutableRef
	sessionID string
	config    ocispec.Image
}

func (layer *sourceLayer) Size() int64 {
	return layer.size
}

func (layer *sourceLayer) Digest() digest.Digest {
	return layer.ref.Info().Blob
}

func (layer *sourceLayer) ChainID() digest.Digest {
	return layer.ref.Info().ChainID
}

func (layer *sourceLayer) ParentChainID() *digest.Digest {
	if layer.ref.Parent() == nil {
		return nil
	}
	chainID := layer.ref.Parent().Info().ChainID
	return &chainID
}

func (layer *sourceLayer) Mount(ctx context.Context) ([]mount.Mount, func() error, error) {
	mountable, err := layer.ref.Mount(ctx, true, session.NewGroup(layer.sessionID))
	if err != nil {
		return nil, nil, errors.Wrap(err, "mount from source ref")
	}

	return mountable.Mount()
}

func (sp *sourceProvider) Manifest(ctx context.Context) (*ocispec.Descriptor, error) {
	return nil, nil
}

func (sp *sourceProvider) Config(ctx context.Context) (*ocispec.Image, error) {
	return &sp.config, nil
}

func (sp *sourceProvider) Layers(ctx context.Context) ([]provider.SourceLayer, error) {
	ref := sp.ref
	layers := []provider.SourceLayer{}

	for ref != nil {
		size, err := ref.Size(ctx)
		if err != nil {
			return nil, errors.Wrap(err, "get size of source ref")
		}
		layer := &sourceLayer{
			ref:       ref,
			sessionID: sp.sessionID,
			size:      size,
		}
		layers = append([]provider.SourceLayer{layer}, layers...)
		ref = ref.Parent()
	}

	return layers, nil
}
