package containerimage

import (
	"context"
	"encoding/json"
	"maps"
	"runtime"
	"time"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/core/leases"
	"github.com/containerd/containerd/v2/core/remotes"
	"github.com/containerd/containerd/v2/core/remotes/docker"
	"github.com/containerd/containerd/v2/core/snapshots"
	"github.com/containerd/containerd/v2/pkg/snapshotters"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb/sourceresolver"
	"github.com/moby/buildkit/exporter"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/solver"
	"github.com/moby/buildkit/solver/errdefs"
	"github.com/moby/buildkit/util/bklog"
	"github.com/moby/buildkit/util/contentutil"
	"github.com/moby/buildkit/util/estargz"
	"github.com/moby/buildkit/util/flightcontrol"
	"github.com/moby/buildkit/util/imageutil"
	"github.com/moby/buildkit/util/leaseutil"
	"github.com/moby/buildkit/util/progress"
	"github.com/moby/buildkit/util/progress/controller"
	"github.com/moby/buildkit/util/pull"
	"github.com/moby/buildkit/util/resolver"
	digest "github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/identity"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
)

type puller struct {
	CacheAccessor  cache.Accessor
	LeaseManager   leases.Manager
	RegistryHosts  docker.RegistryHosts
	ImageStore     images.Store
	Mode           resolver.ResolveMode
	RecordType     client.UsageRecordType
	Ref            string
	SessionManager *session.Manager
	layerLimit     *int
	vtx            solver.Vertex
	ResolverType
	store        sourceresolver.ResolveImageConfigOptStore
	eagerPushCfg *exporter.EagerPushConfig

	g                flightcontrol.Group[struct{}]
	cacheKeyErr      error
	cacheKeyDone     bool
	releaseTmpLeases func(context.Context) error
	descHandlers     cache.DescHandlers
	manifest         *pull.PulledManifests
	manifestKey      string
	configKey        string
	*pull.Puller
}

func (p *puller) SetEagerPushConfig(cfg *exporter.EagerPushConfig) {
	p.eagerPushCfg = cfg
}

func mainManifestKey(desc ocispecs.Descriptor, platform ocispecs.Platform, layerLimit *int) (digest.Digest, error) {
	dt, err := json.Marshal(struct {
		Digest     digest.Digest
		OS         string
		Arch       string
		Variant    string   `json:",omitempty"`
		OSVersion  string   `json:",omitempty"`
		OSFeatures []string `json:",omitempty"`
		Limit      *int     `json:",omitempty"`
	}{
		Digest:     desc.Digest,
		OS:         platform.OS,
		Arch:       platform.Architecture,
		Variant:    platform.Variant,
		OSVersion:  platform.OSVersion,
		OSFeatures: platform.OSFeatures,
		Limit:      layerLimit,
	})
	if err != nil {
		return "", err
	}
	return digest.FromBytes(dt), nil
}

func (p *puller) CacheKey(ctx context.Context, g session.Group, index int) (cacheKey string, imgDigest string, cacheOpts solver.CacheOpts, cacheDone bool, err error) {
	var getResolver pull.SessionResolver
	switch p.ResolverType {
	case ResolverTypeRegistry:
		resolver := resolver.DefaultPool.GetResolver(p.RegistryHosts, p.Ref, "pull", p.SessionManager, g).WithImageStore(p.ImageStore, p.Mode)
		p.Resolver = resolver
		getResolver = func(g session.Group) remotes.Resolver { return resolver.WithSession(g) }
	case ResolverTypeOCILayout:
		resolver := getOCILayoutResolver(p.store, p.SessionManager, g)
		p.Resolver = resolver
		// OCILayout has no need for session
		getResolver = func(g session.Group) remotes.Resolver { return resolver }
	default:
	}

	// progressFactory needs the outer context, the context in `p.g.Do` will
	// be canceled before the progress output is complete
	progressFactory := progress.FromContext(ctx)

	_, err = p.g.Do(ctx, "", func(ctx context.Context) (_ struct{}, err error) {
		if p.cacheKeyErr != nil || p.cacheKeyDone {
			return struct{}{}, p.cacheKeyErr
		}
		defer func() {
			if !errdefs.IsCanceled(ctx, err) {
				p.cacheKeyErr = err
			}
		}()
		ctx, done, err := leaseutil.WithLease(ctx, p.LeaseManager, leases.WithExpiration(5*time.Minute), leaseutil.MakeTemporary)
		if err != nil {
			return struct{}{}, err
		}
		p.releaseTmpLeases = done
		defer imageutil.AddLease(done)

		resolveProgressDone := progress.OneOff(ctx, "resolve "+p.Src.String())
		defer func() {
			resolveProgressDone(err)
		}()

		p.manifest, err = p.PullManifests(ctx, getResolver)
		if err != nil {
			return struct{}{}, err
		}

		if ll := p.layerLimit; ll != nil {
			if *ll > len(p.manifest.Descriptors) {
				return struct{}{}, errors.Errorf("layer limit %d is greater than the number of layers in the image %d", *ll, len(p.manifest.Descriptors))
			}
			p.manifest.Descriptors = p.manifest.Descriptors[:*ll]
		}

		if p.eagerPushCfg != nil && p.eagerPushCfg.PreferPushRegistry && p.ResolverType == ResolverTypeRegistry && len(p.manifest.Descriptors) > 0 {
			p.wrapProviderWithPushFallback(g)
		}

		if len(p.manifest.Descriptors) > 0 {
			progressController := &controller.Controller{
				WriterFactory: progressFactory,
			}
			if p.vtx != nil {
				progressController.Digest = p.vtx.Digest()
				progressController.Name = p.vtx.Name()
				progressController.ProgressGroup = p.vtx.Options().ProgressGroup
			}

			p.descHandlers = cache.DescHandlers(make(map[digest.Digest]*cache.DescHandler))
			for i, desc := range p.manifest.Descriptors {
				labels := snapshots.FilterInheritedLabels(desc.Annotations)
				if labels == nil {
					labels = make(map[string]string)
				}
				maps.Copy(labels, estargz.SnapshotLabels(p.manifest.Ref, p.manifest.Descriptors, i))
				labels[snapshotters.TargetRefLabel] = p.manifest.Ref

				p.descHandlers[desc.Digest] = &cache.DescHandler{
					Provider:       p.manifest.Provider,
					Progress:       progressController,
					SnapshotLabels: labels,
					Annotations:    desc.Annotations,
					Ref:            p.manifest.Ref,
				}
			}
		}

		desc := p.manifest.MainManifestDesc
		k, err := mainManifestKey(desc, p.Platform, p.layerLimit)
		if err != nil {
			return struct{}{}, err
		}
		p.manifestKey = k.String()

		dt, err := content.ReadBlob(ctx, p.ContentStore, p.manifest.ConfigDesc)
		if err != nil {
			return struct{}{}, err
		}
		ck, err := cacheKeyFromConfig(dt, p.layerLimit)
		if err != nil {
			return struct{}{}, err
		}
		p.configKey = ck.String()
		p.cacheKeyDone = true
		return struct{}{}, nil
	})
	if err != nil {
		return "", "", nil, false, err
	}

	cacheOpts = solver.CacheOpts(make(map[any]any))
	for dgst, descHandler := range p.descHandlers {
		cacheOpts[cache.DescHandlerKey(dgst)] = descHandler
	}

	cacheDone = index > 0
	if index == 0 || p.configKey == "" {
		return p.manifestKey, p.manifest.MainManifestDesc.Digest.String(), cacheOpts, cacheDone, nil
	}
	return p.configKey, p.manifest.MainManifestDesc.Digest.String(), cacheOpts, cacheDone, nil
}

func (p *puller) Snapshot(ctx context.Context, g session.Group) (ir cache.ImmutableRef, err error) {
	var getResolver pull.SessionResolver
	switch p.ResolverType {
	case ResolverTypeRegistry:
		resolver := resolver.DefaultPool.GetResolver(p.RegistryHosts, p.Ref, "pull", p.SessionManager, g).WithImageStore(p.ImageStore, p.Mode)
		p.Resolver = resolver
		getResolver = func(g session.Group) remotes.Resolver { return resolver.WithSession(g) }
	case ResolverTypeOCILayout:
		resolver := getOCILayoutResolver(p.store, p.SessionManager, g)
		p.Resolver = resolver
		// OCILayout has no need for session
		getResolver = func(g session.Group) remotes.Resolver { return resolver }
	default:
	}

	if len(p.manifest.Descriptors) == 0 {
		return nil, nil
	}
	defer func() {
		if p.releaseTmpLeases != nil {
			p.releaseTmpLeases(context.WithoutCancel(ctx))
		}
	}()

	var current cache.ImmutableRef
	defer func() {
		if err != nil && current != nil {
			current.Release(context.WithoutCancel(ctx))
		}
	}()

	var parent cache.ImmutableRef
	setWindowsLayerType := p.Platform.OS == "windows" && runtime.GOOS != "windows"
	for _, layerDesc := range p.manifest.Descriptors {
		parent = current
		current, err = p.CacheAccessor.GetByBlob(ctx, layerDesc, parent,
			p.descHandlers, cache.WithImageRef(p.manifest.Ref))
		if parent != nil {
			parent.Release(context.TODO())
		}
		if err != nil {
			return nil, err
		}
		if setWindowsLayerType {
			if err := current.SetLayerType("windows"); err != nil {
				return nil, err
			}
		}
	}

	for _, desc := range p.manifest.Nonlayers {
		if _, err := p.ContentStore.Info(ctx, desc.Digest); cerrdefs.IsNotFound(err) {
			// manifest or config must have gotten gc'd after CacheKey, re-pull them
			ctx, done, err := leaseutil.WithLease(ctx, p.LeaseManager, leaseutil.MakeTemporary)
			if err != nil {
				return nil, err
			}
			defer done(context.WithoutCancel(ctx))

			if _, err := p.PullManifests(ctx, getResolver); err != nil {
				return nil, err
			}
		} else if err != nil {
			return nil, err
		}

		if err := p.LeaseManager.AddResource(ctx, leases.Lease{ID: current.ID()}, leases.Resource{
			ID:   desc.Digest.String(),
			Type: "content",
		}); err != nil {
			return nil, err
		}
	}

	if p.RecordType != "" && current.GetRecordType() == "" {
		if err := current.SetRecordType(p.RecordType); err != nil {
			return nil, err
		}
	}

	return current, nil
}

// wrapProviderWithPushFallback wraps the manifest's Provider so that when
// a layer blob is lazily fetched, it first tries the push (destination)
// registry. If the layer already exists there, it is pulled from the closer
// registry instead of the origin. On any failure, the original provider is
// used transparently.
func (p *puller) wrapProviderWithPushFallback(g session.Group) {
	cfg := p.eagerPushCfg
	pushResolver := resolver.DefaultPool.GetResolver(cfg.RegistryHosts, cfg.TargetName, "pull", p.SessionManager, g)
	origProvider := p.manifest.Provider
	pushRef := cfg.TargetName

	bklog.L.Infof("prefer-push-registry: wrapping provider for %s with fallback to push registry %s", p.Ref, pushRef)

	p.manifest.Provider = func(g session.Group) content.Provider {
		return &pushFallbackProvider{
			pushResolver: pushResolver.WithSession(g),
			pushRef:      pushRef,
			origin:       origProvider(g),
		}
	}
}

// pushFallbackProvider tries fetching a blob from the push registry first,
// falling back to the origin provider on any error (404, auth, network, etc.).
type pushFallbackProvider struct {
	pushResolver remotes.Resolver
	pushRef      string
	origin       content.Provider
}

func (p *pushFallbackProvider) ReaderAt(ctx context.Context, desc ocispecs.Descriptor) (content.ReaderAt, error) {
	fetcher, err := p.pushResolver.Fetcher(ctx, p.pushRef)
	if err == nil {
		ra, fetchErr := contentutil.FromFetcher(fetcher).ReaderAt(ctx, desc)
		if fetchErr == nil {
			bklog.G(ctx).Infof("prefer-push-registry: layer %s pulled from push registry %s", desc.Digest, p.pushRef)
			return ra, nil
		}
		bklog.G(ctx).Infof("prefer-push-registry: layer %s not in push registry %s (%v), falling back to origin", desc.Digest, p.pushRef, fetchErr)
	} else {
		bklog.G(ctx).Infof("prefer-push-registry: could not get fetcher for %s (%v), falling back to origin for layer %s", p.pushRef, err, desc.Digest)
	}
	return p.origin.ReaderAt(ctx, desc)
}

// cacheKeyFromConfig returns a stable digest from image config. If image config
// is a known oci image we will use chainID of layers.
func cacheKeyFromConfig(dt []byte, layerLimit *int) (digest.Digest, error) {
	var img ocispecs.Image
	err := json.Unmarshal(dt, &img)
	if err != nil {
		if layerLimit != nil {
			return "", errors.Wrap(err, "failed to parse image config")
		}
		return digest.FromBytes(dt), nil // digest of config
	}
	if layerLimit != nil {
		l := *layerLimit
		if len(img.RootFS.DiffIDs) < l {
			return "", errors.Errorf("image has %d layers, limit is %d", len(img.RootFS.DiffIDs), l)
		}
		img.RootFS.DiffIDs = img.RootFS.DiffIDs[:l]
	}
	if img.RootFS.Type != "layers" || len(img.RootFS.DiffIDs) == 0 {
		return "", nil
	}

	return identity.ChainID(img.RootFS.DiffIDs), nil
}
