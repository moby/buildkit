package imageutil

import (
	"context"
	"encoding/json"

	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/images"
	"github.com/containerd/containerd/platforms"
	"github.com/containerd/containerd/reference"
	"github.com/containerd/containerd/remotes"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
)

type IngesterProvider interface {
	content.Ingester
	content.Provider
}

func Config(ctx context.Context, str string, resolver remotes.Resolver, ingester IngesterProvider) ([]byte, error) {
	ref, err := reference.Parse(str)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	dgst := ref.Digest()
	var desc *ocispec.Descriptor
	if dgst != "" {
		ra, err := ingester.ReaderAt(ctx, dgst)
		if err == nil {
			mt, err := detectManifestMediaType(ra)
			if err == nil {
				desc = &ocispec.Descriptor{
					Size:      ra.Size(),
					Digest:    dgst,
					MediaType: mt,
				}
			}
		}
	}

	if desc == nil {
		_, desc2, err := resolver.Resolve(ctx, ref.String())
		if err != nil {
			return nil, err
		}
		desc = &desc2
	}

	fetcher, err := resolver.Fetcher(ctx, ref.String())
	if err != nil {
		return nil, err
	}

	handlers := []images.Handler{
		remotes.FetchHandler(ingester, fetcher),
		childrenConfigHandler(ingester),
	}
	if err := images.Dispatch(ctx, images.Handlers(handlers...), *desc); err != nil {
		return nil, err
	}
	config, err := images.Config(ctx, ingester, *desc, platforms.Format(platforms.Default()))
	if err != nil {
		return nil, err
	}

	return content.ReadBlob(ctx, ingester, config.Digest)
}

func childrenConfigHandler(provider content.Provider) images.HandlerFunc {
	return func(ctx context.Context, desc ocispec.Descriptor) ([]ocispec.Descriptor, error) {
		var descs []ocispec.Descriptor
		switch desc.MediaType {
		case images.MediaTypeDockerSchema2Manifest, ocispec.MediaTypeImageManifest:
			p, err := content.ReadBlob(ctx, provider, desc.Digest)
			if err != nil {
				return nil, err
			}

			// TODO(stevvooe): We just assume oci manifest, for now. There may be
			// subtle differences from the docker version.
			var manifest ocispec.Manifest
			if err := json.Unmarshal(p, &manifest); err != nil {
				return nil, err
			}

			descs = append(descs, manifest.Config)
		case images.MediaTypeDockerSchema2ManifestList, ocispec.MediaTypeImageIndex:
			p, err := content.ReadBlob(ctx, provider, desc.Digest)
			if err != nil {
				return nil, err
			}

			var index ocispec.Index
			if err := json.Unmarshal(p, &index); err != nil {
				return nil, err
			}

			descs = append(descs, index.Manifests...)
		case images.MediaTypeDockerSchema2Config, ocispec.MediaTypeImageConfig:
			// childless data types.
			return nil, nil
		default:
			return nil, errors.Errorf("encountered unknown type %v; children may not be fetched", desc.MediaType)
		}

		return descs, nil
	}
}

// ocispec.MediaTypeImageManifest, // TODO: detect schema1/manifest-list
func detectManifestMediaType(ra content.ReaderAt) (string, error) {
	// TODO: schema1

	p := make([]byte, ra.Size())
	if _, err := ra.ReadAt(p, 0); err != nil {
		return "", err
	}

	var mfst struct {
		Config json.RawMessage `json:"config"`
	}

	if err := json.Unmarshal(p, &mfst); err != nil {
		return "", err
	}

	if mfst.Config != nil {
		return ocispec.MediaTypeImageManifest, nil
	}
	return ocispec.MediaTypeImageIndex, nil
}
