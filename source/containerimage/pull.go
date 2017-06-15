package containerimage

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/images"
	"github.com/containerd/containerd/remotes"
	"github.com/containerd/containerd/remotes/docker"
	"github.com/containerd/containerd/rootfs"
	"github.com/containerd/containerd/snapshot"
	digest "github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/identity"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/tonistiigi/buildkit_poc/cache"
	"github.com/tonistiigi/buildkit_poc/source"
	"github.com/tonistiigi/buildkit_poc/util/progress"
)

// TODO: break apart containerd specifics like contentstore so the resolver
// code can be used with any implementation

type SourceOpt struct {
	Snapshotter   snapshot.Snapshotter
	ContentStore  content.Store
	Applier       rootfs.Applier
	CacheAccessor cache.Accessor
}

type blobmapper interface {
	GetBlob(ctx context.Context, key string) (digest.Digest, error)
	SetBlob(ctx context.Context, key string, blob digest.Digest) error
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

	if _, ok := opt.Snapshotter.(blobmapper); !ok {
		return nil, errors.Errorf("imagesource requires snapshotter with blobs mapping support")
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

	resolveProgressDone := oneOffProgress(ctx, "resolve "+imageIdentifier.Reference.String())
	ref, desc, err := is.resolver.Resolve(ctx, imageIdentifier.Reference.String())
	if err != nil {
		return nil, resolveProgressDone(err)
	}
	resolveProgressDone(nil)

	ongoing := newJobs(ref)
	pctx, stopProgress := context.WithCancel(ctx)

	go showProgress(pctx, ongoing, is.ContentStore)

	fetcher, err := is.resolver.Fetcher(ctx, ref)
	if err != nil {
		return nil, err
	}

	// TODO: need a wrapper snapshot interface that combines content
	// and snapshots as 1) buildkit shouldn't have a dependency on contentstore
	// or 2) cachemanager should manage the contentstore
	handlers := []images.Handler{
		images.HandlerFunc(func(ctx context.Context, desc ocispec.Descriptor) ([]ocispec.Descriptor, error) {
			ongoing.add(ctx, desc)
			return nil, nil
		}),
		remotes.FetchHandler(is.ContentStore, fetcher),
		images.ChildrenHandler(is.ContentStore),
	}
	if err := images.Dispatch(ctx, images.Handlers(handlers...), desc); err != nil {
		return nil, err
	}
	stopProgress()

	unpackProgressDone := oneOffProgress(ctx, "unpacking "+imageIdentifier.Reference.String())
	chainid, err := is.unpack(ctx, desc)
	if err != nil {
		return nil, unpackProgressDone(err)
	}
	unpackProgressDone(nil)

	return is.CacheAccessor.Get(ctx, chainid)
}

func (is *imageSource) unpack(ctx context.Context, desc ocispec.Descriptor) (string, error) {
	layers, err := getLayers(ctx, is.ContentStore, desc)
	if err != nil {
		return "", err
	}

	chainID, err := rootfs.ApplyLayers(ctx, layers, is.Snapshotter, is.Applier)
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
		if err := is.SourceOpt.Snapshotter.(blobmapper).SetBlob(ctx, string(chainID), l.Blob.Digest); err != nil {
			return err
		}
	}
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

func showProgress(ctx context.Context, ongoing *jobs, cs content.Store) {
	var (
		ticker = time.NewTicker(100 * time.Millisecond)
		// fw       = progress.NewWriter(out)
		statuses = map[string]statusInfo{}
		done     bool
	)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
		case <-ctx.Done():
			done = true
		}
		// fw.Flush()

		// tw := tabwriter.NewWriter(fw, 1, 8, 1, ' ', 0)

		resolved := "resolved"
		if !ongoing.isResolved() {
			resolved = "resolving"
		}
		statuses[ongoing.name] = statusInfo{
			Ref:    ongoing.name,
			Status: resolved,
		}
		// keys := []string{ongoing.name}

		actives := make(map[string]statusInfo)

		if !done {
			active, err := cs.Status(ctx, "")
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
				j.Write(progress.Status{
					Action:  a.Status,
					Total:   int(a.Total),
					Current: int(a.Offset),
					Started: &j.started,
				})
				continue
			}

			if !j.done {
				info, err := cs.Info(ctx, j.Digest)
				if err != nil {
					if content.IsNotFound(err) {
						j.Write(progress.Status{
							Action: "waiting",
						})
						continue
					}
				} else {
					j.done = true
				}

				if done || j.done {
					j.Write(progress.Status{
						Action:    "done",
						Current:   int(info.Size),
						Total:     int(info.Size),
						Completed: &info.CommittedAt,
						Started:   &j.started,
					})
					j.Done()
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
	progress.ProgressWriter
	done    bool
	started time.Time
}

func newJobs(name string) *jobs {
	return &jobs{
		name:  name,
		added: make(map[digest.Digest]job),
	}
}

func (j *jobs) add(ctx context.Context, desc ocispec.Descriptor) {
	j.mu.Lock()
	defer j.mu.Unlock()

	if _, ok := j.added[desc.Digest]; ok {
		return
	}
	pw, _, _ := progress.FromContext(ctx, desc.Digest.String())
	j.added[desc.Digest] = job{
		Descriptor:     desc,
		ProgressWriter: pw,
		started:        time.Now(),
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
	pw, _, _ := progress.FromContext(ctx, id)
	now := time.Now()
	st := progress.Status{
		Action:  id,
		Started: &now,
	}
	pw.Write(st)
	return func(err error) error {
		// TODO: set error on status
		now := time.Now()
		st.Completed = &now
		pw.Write(st)
		pw.Done()
		return err
	}
}
