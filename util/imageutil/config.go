package imageutil

import (
	"context"
	"encoding/json"
	"io"

	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/images"
	"github.com/containerd/containerd/remotes"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
)

type IngesterProvider interface {
	content.Ingester
	content.Provider
}

func Config(ctx context.Context, ref string, resolver remotes.Resolver, ingester IngesterProvider) (*ocispec.Image, error) {
	ref, desc, err := resolver.Resolve(ctx, ref)
	if err != nil {
		return nil, err
	}

	fetcher, err := resolver.Fetcher(ctx, ref)
	if err != nil {
		return nil, err
	}

	handlers := []images.Handler{
		remotes.FetchHandler(ingester, fetcher),
		childrenConfigHandler(ingester),
	}
	if err := images.Dispatch(ctx, images.Handlers(handlers...), desc); err != nil {
		return nil, err
	}
	config, err := images.Config(ctx, ingester, desc)
	if err != nil {
		return nil, err
	}

	var ociimage ocispec.Image

	r, err := ingester.ReaderAt(ctx, config.Digest)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	dec := json.NewDecoder(readerAtToReader(r))
	if err := dec.Decode(&ociimage); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, errors.New("invalid image config")
	}

	return &ociimage, nil
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

func readerAtToReader(r io.ReaderAt) io.Reader {
	return &reader{ra: r}
}

type reader struct {
	ra     io.ReaderAt
	offset int64
}

func (r *reader) Read(b []byte) (int, error) {
	n, err := r.ra.ReadAt(b, r.offset)
	r.offset += int64(n)
	return n, err
}
