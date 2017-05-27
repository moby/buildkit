package containerimage

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/images"
	"github.com/containerd/containerd/remotes"
	"github.com/containerd/containerd/remotes/docker"
	"github.com/containerd/containerd/rootfs"
	"github.com/containerd/containerd/snapshot"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/tonistiigi/buildkit_poc/cache"
	"github.com/tonistiigi/buildkit_poc/source"
)

// TODO: break apart containerd specifics like contentstore so the resolver
// code can be used with any implementation

type SourceOpt struct {
	Snapshotter  snapshot.Snapshotter
	ContentStore content.Store
	Applier      rootfs.Applier
	Accessor     cache.Accessor
}

type imageSource struct {
	SourceOpt
	resolver remotes.Resolver
}

func NewSource(opt SourceOpt) (source.Source, error) {
	is := &imageSource{
		SourceOpt: opt,
		resolver: docker.NewResolver(docker.ResolverOptions{
			Client: http.DefaultClient,
		}),
	}
	return is, nil
}

func (is *imageSource) ID() string {
	return source.DockerImageScheme
}

func (is *imageSource) Pull(ctx context.Context, id source.Identifier) (cache.ImmutableRef, error) {
	// TODO: update this to always centralize layer downloads/unpacks
	// TODO: progress status

	imageIdentifier, ok := id.(*source.ImageIdentifier)
	if !ok {
		return nil, errors.New("invalid identifier")
	}

	ref, desc, err := is.resolver.Resolve(ctx, imageIdentifier.Reference.String())
	if err != nil {
		return nil, err
	}
	fetcher, err := is.resolver.Fetcher(ctx, ref)
	if err != nil {
		return nil, err
	}

	handlers := []images.Handler{
		remotes.FetchHandler(is.ContentStore, fetcher),
		images.ChildrenHandler(is.ContentStore),
	}
	if err := images.Dispatch(ctx, images.Handlers(handlers...), desc); err != nil {
		return nil, err
	}

	if err := is.unpack(ctx, desc); err != nil {
		return nil, err
	}
	return nil, nil
}

func (is *imageSource) unpack(ctx context.Context, desc ocispec.Descriptor) error {
	layers, err := getLayers(ctx, is.ContentStore, desc)
	if err != nil {
		return err
	}

	chainid, err := rootfs.ApplyLayers(ctx, layers, is.Snapshotter, is.Applier)
	if err != nil {
		return err
	}
	_ = chainid
	return nil
}

func getLayers(ctx context.Context, provider content.Provider, desc ocispec.Descriptor) ([]rootfs.Layer, error) {
	p, err := content.ReadBlob(ctx, provider, desc.Digest)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to read manifest blob")
	}
	var manifest ocispec.Manifest
	if err := json.Unmarshal(p, &manifest); err != nil {
		return nil, errors.Wrap(err, "failed to unmarshal manifest")
	}
	image := images.Image{Target: desc}
	diffIDs, err := image.RootFS(ctx, provider)
	if err != nil {
		return nil, errors.Wrap(err, "failed to resolve rootfs")
	}
	if len(diffIDs) != len(manifest.Layers) {
		return nil, errors.Errorf("mismatched image rootfs and manifest layers")
	}
	layers := make([]rootfs.Layer, len(diffIDs))
	for i := range diffIDs {
		layers[i].Diff = ocispec.Descriptor{
			// TODO: derive media type from compressed type
			MediaType: ocispec.MediaTypeImageLayer,
			Digest:    diffIDs[i],
		}
		layers[i].Blob = manifest.Layers[i]
	}
	return layers, nil
}
