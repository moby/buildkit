package containerimage

import (
	gocontext "context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/diff"
	"github.com/containerd/containerd/errdefs"
	"github.com/containerd/containerd/images"
	"github.com/containerd/containerd/platforms"
	"github.com/containerd/containerd/remotes"
	"github.com/containerd/containerd/remotes/docker"
	"github.com/containerd/containerd/remotes/docker/schema1"
	"github.com/containerd/containerd/rootfs"
	"github.com/containerd/containerd/snapshots"
	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/auth"
	"github.com/moby/buildkit/snapshot"
	"github.com/moby/buildkit/source"
	"github.com/moby/buildkit/util/flightcontrol"
	"github.com/moby/buildkit/util/imageutil"
	"github.com/moby/buildkit/util/progress"
	digest "github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/identity"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"golang.org/x/net/context"
)

// TODO: break apart containerd specifics like contentstore so the resolver
// code can be used with any implementation

type SourceOpt struct {
	SessionManager *session.Manager
	Snapshotter    snapshot.Snapshotter
	ContentStore   content.Store
	Applier        diff.Differ
	CacheAccessor  cache.Accessor
}

type resolveRecord struct {
	desc ocispec.Descriptor
	ts   time.Time
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

func (is *imageSource) getResolver(ctx context.Context) remotes.Resolver {
	return docker.NewResolver(docker.ResolverOptions{
		Client:      http.DefaultClient,
		Credentials: is.getCredentialsFromSession(ctx),
	})
}

func (is *imageSource) getCredentialsFromSession(ctx context.Context) func(string) (string, string, error) {
	id := session.FromContext(ctx)
	if id == "" {
		return nil
	}
	return func(host string) (string, string, error) {
		timeoutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		caller, err := is.SessionManager.Get(timeoutCtx, id)
		if err != nil {
			return "", "", err
		}

		return auth.CredentialsFunc(context.TODO(), caller)(host)
	}
}

func (is *imageSource) ResolveImageConfig(ctx context.Context, ref string) (digest.Digest, []byte, error) {
	type t struct {
		dgst digest.Digest
		dt   []byte
	}
	res, err := is.g.Do(ctx, ref, func(ctx context.Context) (interface{}, error) {
		dgst, dt, err := imageutil.Config(ctx, ref, is.getResolver(ctx), is.ContentStore)
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

func (is *imageSource) Resolve(ctx context.Context, id source.Identifier) (source.SourceInstance, error) {
	imageIdentifier, ok := id.(*source.ImageIdentifier)
	if !ok {
		return nil, errors.Errorf("invalid image identifier %v", id)
	}

	p := &puller{
		src:      imageIdentifier,
		is:       is,
		resolver: is.getResolver(ctx),
	}
	return p, nil
}

type puller struct {
	is          *imageSource
	resolveOnce sync.Once
	src         *source.ImageIdentifier
	desc        ocispec.Descriptor
	ref         string
	resolveErr  error
	resolver    remotes.Resolver
}

func (p *puller) resolve(ctx context.Context) error {
	p.resolveOnce.Do(func() {
		resolveProgressDone := oneOffProgress(ctx, "resolve "+p.src.Reference.String())

		dgst := p.src.Reference.Digest()
		if dgst != "" {
			info, err := p.is.ContentStore.Info(ctx, dgst)
			if err == nil {
				p.ref = p.src.Reference.String()
				ra, err := p.is.ContentStore.ReaderAt(ctx, dgst)
				if err == nil {
					mt, err := imageutil.DetectManifestMediaType(ra)
					if err == nil {
						p.desc = ocispec.Descriptor{
							Size:      info.Size,
							Digest:    dgst,
							MediaType: mt,
						}
						resolveProgressDone(nil)
						return
					}
				}
			}
		}

		ref, desc, err := p.resolver.Resolve(ctx, p.src.Reference.String())
		if err != nil {
			p.resolveErr = err
			resolveProgressDone(err)
			return
		}
		p.desc = desc
		p.ref = ref
		resolveProgressDone(nil)
	})
	return p.resolveErr
}

func (p *puller) CacheKey(ctx context.Context) (string, error) {
	if err := p.resolve(ctx); err != nil {
		return "", err
	}
	return p.desc.Digest.String(), nil
}

func (p *puller) Snapshot(ctx context.Context) (cache.ImmutableRef, error) {
	if err := p.resolve(ctx); err != nil {
		return nil, err
	}

	ongoing := newJobs(p.ref)

	pctx, stopProgress := context.WithCancel(ctx)

	go showProgress(pctx, ongoing, p.is.ContentStore)

	fetcher, err := p.resolver.Fetcher(ctx, p.ref)
	if err != nil {
		stopProgress()
		return nil, err
	}

	// TODO: need a wrapper snapshot interface that combines content
	// and snapshots as 1) buildkit shouldn't have a dependency on contentstore
	// or 2) cachemanager should manage the contentstore
	handlers := []images.Handler{
		images.HandlerFunc(func(ctx gocontext.Context, desc ocispec.Descriptor) ([]ocispec.Descriptor, error) {
			ongoing.add(desc)
			return nil, nil
		}),
	}
	var schema1Converter *schema1.Converter
	if p.desc.MediaType == images.MediaTypeDockerSchema1Manifest {
		schema1Converter = schema1.NewConverter(p.is.ContentStore, fetcher)
		handlers = append(handlers, schema1Converter)
	} else {
		handlers = append(handlers,
			remotes.FetchHandler(p.is.ContentStore, fetcher),
			images.ChildrenHandler(p.is.ContentStore, platforms.Default()),
		)
	}

	if err := images.Dispatch(ctx, images.Handlers(handlers...), p.desc); err != nil {
		stopProgress()
		return nil, err
	}
	stopProgress()

	if schema1Converter != nil {
		p.desc, err = schema1Converter.Convert(ctx)
		if err != nil {
			return nil, err
		}
	}

	// split all pulled data to layers and rest. layers remain roots and are deleted with snapshots. rest will be linked to layers.
	var notLayerBlobs []ocispec.Descriptor
	var layerBlobs []ocispec.Descriptor
	for _, j := range ongoing.added {
		switch j.MediaType {
		case ocispec.MediaTypeImageLayer, images.MediaTypeDockerSchema2Layer, ocispec.MediaTypeImageLayerGzip, images.MediaTypeDockerSchema2LayerGzip:
			layerBlobs = append(layerBlobs, j.Descriptor)
		default:
			notLayerBlobs = append(notLayerBlobs, j.Descriptor)
		}
	}

	for _, l := range layerBlobs {
		labels := map[string]string{}
		var fields []string
		for _, nl := range notLayerBlobs {
			k := "containerd.io/gc.ref.content." + nl.Digest.Hex()[:12]
			labels[k] = nl.Digest.String()
			fields = append(fields, "labels."+k)
		}
		if _, err := p.is.ContentStore.Update(ctx, content.Info{
			Digest: l.Digest,
			Labels: labels,
		}, fields...); err != nil {
			return nil, err
		}
	}

	for _, nl := range notLayerBlobs {
		if err := p.is.ContentStore.Delete(ctx, nl.Digest); err != nil {
			return nil, err
		}
	}

	unpackProgressDone := oneOffProgress(ctx, "unpacking "+p.src.Reference.String())
	chainid, err := p.is.unpack(ctx, p.desc)
	if err != nil {
		return nil, unpackProgressDone(err)
	}
	unpackProgressDone(nil)

	return p.is.CacheAccessor.Get(ctx, chainid, cache.WithDescription(fmt.Sprintf("pulled from %s", p.ref)))
}

func (is *imageSource) unpack(ctx context.Context, desc ocispec.Descriptor) (string, error) {
	layers, err := getLayers(ctx, is.ContentStore, desc)
	if err != nil {
		return "", err
	}

	var chain []digest.Digest
	for _, layer := range layers {
		labels := map[string]string{
			"containerd.io/gc.root":      time.Now().UTC().Format(time.RFC3339Nano),
			"containerd.io/uncompressed": layer.Diff.Digest.String(),
		}
		if _, err := rootfs.ApplyLayer(ctx, layer, chain, is.Snapshotter, is.Applier, snapshots.WithLabels(labels)); err != nil {
			return "", err
		}
		chain = append(chain, layer.Diff.Digest)
	}
	chainID := identity.ChainID(chain)
	if err != nil {
		return "", err
	}

	if err := is.fillBlobMapping(ctx, layers); err != nil {
		return "", err
	}

	return string(chainID), nil
}

func (is *imageSource) fillBlobMapping(ctx context.Context, layers []rootfs.Layer) error {
	var chain []digest.Digest
	for _, l := range layers {
		chain = append(chain, l.Diff.Digest)
		chainID := identity.ChainID(chain)
		if err := is.SourceOpt.Snapshotter.SetBlob(ctx, string(chainID), l.Diff.Digest, l.Blob.Digest); err != nil {
			return err
		}
	}
	return nil
}

func getLayers(ctx context.Context, provider content.Provider, desc ocispec.Descriptor) ([]rootfs.Layer, error) {
	manifest, err := images.Manifest(ctx, provider, desc, platforms.Default())
	if err != nil {
		return nil, errors.WithStack(err)
	}
	image := images.Image{Target: desc}
	diffIDs, err := image.RootFS(ctx, provider, platforms.Default())
	if err != nil {
		return nil, errors.Wrap(err, "failed to resolve rootfs")
	}
	if len(diffIDs) != len(manifest.Layers) {
		return nil, errors.Errorf("mismatched image rootfs and manifest layers %+v %+v", diffIDs, manifest.Layers)
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

func showProgress(ctx context.Context, ongoing *jobs, cs content.Store) {
	var (
		ticker   = time.NewTicker(100 * time.Millisecond)
		statuses = map[string]statusInfo{}
		done     bool
	)
	defer ticker.Stop()

	pw, _, ctx := progress.FromContext(ctx)
	defer pw.Close()

	for {
		select {
		case <-ticker.C:
		case <-ctx.Done():
			done = true
		}

		resolved := "resolved"
		if !ongoing.isResolved() {
			resolved = "resolving"
		}
		statuses[ongoing.name] = statusInfo{
			Ref:    ongoing.name,
			Status: resolved,
		}

		actives := make(map[string]statusInfo)

		if !done {
			active, err := cs.ListStatuses(ctx, "")
			if err != nil {
				// log.G(ctx).WithError(err).Error("active check failed")
				continue
			}
			// update status of active entries!
			for _, active := range active {
				actives[active.Ref] = statusInfo{
					Ref:       active.Ref,
					Status:    "downloading",
					Offset:    active.Offset,
					Total:     active.Total,
					StartedAt: active.StartedAt,
					UpdatedAt: active.UpdatedAt,
				}
			}
		}

		// now, update the items in jobs that are not in active
		for _, j := range ongoing.jobs() {
			refKey := remotes.MakeRefKey(ctx, j.Descriptor)
			if a, ok := actives[refKey]; ok {
				started := j.started
				pw.Write(j.Digest.String(), progress.Status{
					Action:  a.Status,
					Total:   int(a.Total),
					Current: int(a.Offset),
					Started: &started,
				})
				continue
			}

			if !j.done {
				info, err := cs.Info(context.TODO(), j.Digest)
				if err != nil {
					if errdefs.IsNotFound(err) {
						pw.Write(j.Digest.String(), progress.Status{
							Action: "waiting",
						})
						continue
					}
				} else {
					j.done = true
				}

				if done || j.done {
					started := j.started
					createdAt := info.CreatedAt
					pw.Write(j.Digest.String(), progress.Status{
						Action:    "done",
						Current:   int(info.Size),
						Total:     int(info.Size),
						Completed: &createdAt,
						Started:   &started,
					})
				}
			}
		}
		if done {
			return
		}
	}
}

// jobs provides a way of identifying the download keys for a particular task
// encountering during the pull walk.
//
// This is very minimal and will probably be replaced with something more
// featured.
type jobs struct {
	name     string
	added    map[digest.Digest]job
	mu       sync.Mutex
	resolved bool
}

type job struct {
	ocispec.Descriptor
	done    bool
	started time.Time
}

func newJobs(name string) *jobs {
	return &jobs{
		name:  name,
		added: make(map[digest.Digest]job),
	}
}

func (j *jobs) add(desc ocispec.Descriptor) {
	j.mu.Lock()
	defer j.mu.Unlock()

	if _, ok := j.added[desc.Digest]; ok {
		return
	}
	j.added[desc.Digest] = job{
		Descriptor: desc,
		started:    time.Now(),
	}
}

func (j *jobs) jobs() []job {
	j.mu.Lock()
	defer j.mu.Unlock()

	descs := make([]job, 0, len(j.added))
	for _, j := range j.added {
		descs = append(descs, j)
	}
	return descs
}

func (j *jobs) isResolved() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.resolved
}

type statusInfo struct {
	Ref       string
	Status    string
	Offset    int64
	Total     int64
	StartedAt time.Time
	UpdatedAt time.Time
}

func oneOffProgress(ctx context.Context, id string) func(err error) error {
	pw, _, _ := progress.FromContext(ctx)
	now := time.Now()
	st := progress.Status{
		Started: &now,
	}
	pw.Write(id, st)
	return func(err error) error {
		// TODO: set error on status
		now := time.Now()
		st.Completed = &now
		pw.Write(id, st)
		pw.Close()
		return err
	}
}
