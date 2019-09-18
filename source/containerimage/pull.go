package containerimage

import (
	"context"
	"encoding/json"
	"runtime"

	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/diff"
	"github.com/containerd/containerd/images"
	"github.com/containerd/containerd/leases"
	"github.com/containerd/containerd/platforms"
	"github.com/docker/distribution/reference"
	"github.com/moby/buildkit/cache"
	gw "github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/snapshot"
	"github.com/moby/buildkit/source"
	"github.com/moby/buildkit/util/flightcontrol"
	"github.com/moby/buildkit/util/imageutil"
	"github.com/moby/buildkit/util/leaseutil"
	"github.com/moby/buildkit/util/pull"
	"github.com/moby/buildkit/util/resolver"
	"github.com/moby/buildkit/util/winlayers"
	digest "github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/identity"
	specs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
)

// TODO: break apart containerd specifics like contentstore so the resolver
// code can be used with any implementation

type SourceOpt struct {
	Snapshotter   snapshot.Snapshotter
	ContentStore  content.Store
	Applier       diff.Applier
	CacheAccessor cache.Accessor
	ImageStore    images.Store // optional
	ResolverOpt   resolver.ResolveOptionsFunc
	LeaseManager  leases.Manager
}

type imageSource struct {
	SourceOpt
	g flightcontrol.Group
}

func NewSource(opt SourceOpt) (source.Source, error) {
	is := &imageSource{
		SourceOpt: opt,
	}

	return is, nil
}

func (is *imageSource) ID() string {
	return source.DockerImageScheme
}

func (is *imageSource) ResolveImageConfig(ctx context.Context, ref string, opt gw.ResolveImageConfigOpt, sm *session.Manager) (digest.Digest, []byte, error) {
	type t struct {
		dgst digest.Digest
		dt   []byte
	}
	key := ref
	if platform := opt.Platform; platform != nil {
		key += platforms.Format(*platform)
	}

	rm, err := source.ParseImageResolveMode(opt.ResolveMode)
	if err != nil {
		return "", nil, err
	}

	res, err := is.g.Do(ctx, key, func(ctx context.Context) (interface{}, error) {
		dgst, dt, err := imageutil.Config(ctx, ref, pull.NewResolver(ctx, is.ResolverOpt, sm, is.ImageStore, rm, ref), is.ContentStore, is.LeaseManager, opt.Platform)
		if err != nil {
			return nil, err
		}
		return &t{dgst: dgst, dt: dt}, nil
	})
	if err != nil {
		return "", nil, err
	}
	typed := res.(*t)
	return typed.dgst, typed.dt, nil
}

func (is *imageSource) Resolve(ctx context.Context, id source.Identifier, sm *session.Manager) (source.SourceInstance, error) {
	imageIdentifier, ok := id.(*source.ImageIdentifier)
	if !ok {
		return nil, errors.Errorf("invalid image identifier %v", id)
	}

	platform := platforms.DefaultSpec()
	if imageIdentifier.Platform != nil {
		platform = *imageIdentifier.Platform
	}

	pullerUtil := &pull.Puller{
		Snapshotter:  is.Snapshotter,
		ContentStore: is.ContentStore,
		Applier:      is.Applier,
		Src:          imageIdentifier.Reference,
		Resolver:     pull.NewResolver(ctx, is.ResolverOpt, sm, is.ImageStore, imageIdentifier.ResolveMode, imageIdentifier.Reference.String()),
		Platform:     &platform,
	}
	p := &puller{
		CacheAccessor: is.CacheAccessor,
		Puller:        pullerUtil,
		Platform:      platform,
		id:            imageIdentifier,
		LeaseManager:  is.LeaseManager,
	}
	return p, nil
}

type puller struct {
	CacheAccessor cache.Accessor
	LeaseManager  leases.Manager
	Platform      specs.Platform
	id            *source.ImageIdentifier
	*pull.Puller
}

func mainManifestKey(ctx context.Context, desc specs.Descriptor, platform specs.Platform) (digest.Digest, error) {
	dt, err := json.Marshal(struct {
		Digest  digest.Digest
		OS      string
		Arch    string
		Variant string `json:",omitempty"`
	}{
		Digest:  desc.Digest,
		OS:      platform.OS,
		Arch:    platform.Architecture,
		Variant: platform.Variant,
	})
	if err != nil {
		return "", err
	}
	return digest.FromBytes(dt), nil
}

func (p *puller) CacheKey(ctx context.Context, index int) (string, bool, error) {
	_, desc, err := p.Puller.Resolve(ctx)
	if err != nil {
		return "", false, err
	}
	if index == 0 || desc.Digest == "" {
		k, err := mainManifestKey(ctx, desc, p.Platform)
		if err != nil {
			return "", false, err
		}
		return k.String(), false, nil
	}
	ref, err := reference.ParseNormalizedNamed(p.Src.String())
	if err != nil {
		return "", false, err
	}
	ref, err = reference.WithDigest(ref, desc.Digest)
	if err != nil {
		return "", false, nil
	}
	_, dt, err := imageutil.Config(ctx, ref.String(), p.Resolver, p.ContentStore, p.LeaseManager, &p.Platform)
	if err != nil {
		return "", false, err
	}

	k := cacheKeyFromConfig(dt).String()
	if k == "" {
		k, err := mainManifestKey(ctx, desc, p.Platform)
		if err != nil {
			return "", false, err
		}
		return k.String(), true, nil
	}
	return k, true, nil
}

func (p *puller) Snapshot(ctx context.Context) (cache.ImmutableRef, error) {
	layerNeedsTypeWindows := false
	if platform := p.Puller.Platform; platform != nil {
		if platform.OS == "windows" && runtime.GOOS != "windows" {
			ctx = winlayers.UseWindowsLayerMode(ctx)
			layerNeedsTypeWindows = true
		}
	}

	// workaround for gcr, authentication not supported on blob endpoints
	pull.EnsureManifestRequested(ctx, p.Puller.Resolver, p.Puller.Src.String())

	ctx, done, err := leaseutil.WithLease(ctx, p.LeaseManager)
	if err != nil {
		return nil, err
	}
	defer done(ctx)

	pulled, err := p.Puller.Pull(ctx)
	if err != nil {
		return nil, err
	}
	if len(pulled.Layers) == 0 {
		return nil, nil
	}

	var current cache.ImmutableRef
	for _, l := range pulled.Layers {
		ref, err := p.CacheAccessor.GetByBlob(ctx, l, current, cache.WithDescription("pulled from "+pulled.Ref))
		if current != nil {
			current.Release(context.TODO())
		}
		if err != nil {
			return nil, err
		}
		if err := ref.Extract(ctx); err != nil {
			ref.Release(context.TODO())
			return nil, err
		}
		current = ref
	}

	if layerNeedsTypeWindows && current != nil {
		if err := markRefLayerTypeWindows(current); err != nil {
			current.Release(context.TODO())
			return nil, err
		}
	}

	if p.id.RecordType != "" && cache.GetRecordType(current) == "" {
		if err := cache.SetRecordType(current, p.id.RecordType); err != nil {
			current.Release(context.TODO())
			return nil, err
		}
	}

	return current, nil
}

func markRefLayerTypeWindows(ref cache.ImmutableRef) error {
	if parent := ref.Parent(); parent != nil {
		defer parent.Release(context.TODO())
		if err := markRefLayerTypeWindows(parent); err != nil {
			return err
		}
	}
	return cache.SetLayerType(ref, "windows")
}

// cacheKeyFromConfig returns a stable digest from image config. If image config
// is a known oci image we will use chainID of layers.
func cacheKeyFromConfig(dt []byte) digest.Digest {
	var img specs.Image
	err := json.Unmarshal(dt, &img)
	if err != nil {
		return digest.FromBytes(dt)
	}
	if img.RootFS.Type != "layers" || len(img.RootFS.DiffIDs) == 0 {
		return ""
	}
	return identity.ChainID(img.RootFS.DiffIDs)
}
