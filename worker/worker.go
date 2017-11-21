package worker

import (
	"golang.org/x/net/context"

	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/diff"
	"github.com/containerd/containerd/images"
	ctdsnapshot "github.com/containerd/containerd/snapshots"
	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/cache/cacheimport"
	"github.com/moby/buildkit/cache/instructioncache"
	"github.com/moby/buildkit/cache/metadata"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/executor"
	"github.com/moby/buildkit/exporter"
	imageexporter "github.com/moby/buildkit/exporter/containerimage"
	localexporter "github.com/moby/buildkit/exporter/local"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/snapshot/blobmapping"
	"github.com/moby/buildkit/source"
	"github.com/moby/buildkit/source/containerimage"
	"github.com/moby/buildkit/source/git"
	"github.com/moby/buildkit/source/http"
	"github.com/moby/buildkit/source/local"
	digest "github.com/opencontainers/go-digest"
)

// WorkerOpt is specific to a worker.
// See also CommonOpt.
type WorkerOpt struct {
	Name            string
	SessionManager  *session.Manager
	MetadataStore   *metadata.Store
	Executor        executor.Executor
	BaseSnapshotter ctdsnapshot.Snapshotter // not blobmapping one (FIXME: just require blobmapping snapshotter?)
	ContentStore    content.Store
	Applier         diff.Differ
	Differ          diff.Differ
	ImageStore      images.Store
}

// Worker is a local worker instance with dedicated snapshotter, cache, and so on.
// TODO: s/Worker/OpWorker/g ?
// FIXME: Worker should be rather an interface
type Worker struct {
	WorkerOpt
	Snapshotter      ctdsnapshot.Snapshotter // blobmapping snapshotter
	CacheManager     cache.Manager
	SourceManager    *source.Manager
	InstructionCache InstructionCache
	Exporters        map[string]exporter.Exporter
	ImageSource      source.Source
	CacheExporter    *cacheimport.CacheExporter
	CacheImporter    *cacheimport.CacheImporter
	// no frontend here
}

// FIXME: deduplicate interface definitions?
type InstructionCache interface {
	Probe(ctx context.Context, key digest.Digest) (bool, error)
	Lookup(ctx context.Context, key digest.Digest, msg string) (interface{}, error) // TODO: regular ref
	Set(key digest.Digest, ref interface{}) error
	SetContentMapping(contentKey, key digest.Digest) error
	GetContentMapping(dgst digest.Digest) ([]digest.Digest, error)
}

// NewWorker instantiates a local worker
func NewWorker(opt WorkerOpt) (*Worker, error) {
	bmSnapshotter, err := blobmapping.NewSnapshotter(blobmapping.Opt{
		Content:       opt.ContentStore,
		Snapshotter:   opt.BaseSnapshotter,
		MetadataStore: opt.MetadataStore,
	})
	if err != nil {
		return nil, err
	}

	cm, err := cache.NewManager(cache.ManagerOpt{
		Snapshotter:   bmSnapshotter,
		MetadataStore: opt.MetadataStore,
	})
	if err != nil {
		return nil, err
	}

	ic := &instructioncache.LocalStore{
		MetadataStore: opt.MetadataStore,
		Cache:         cm,
	}

	sm, err := source.NewManager()
	if err != nil {
		return nil, err
	}

	is, err := containerimage.NewSource(containerimage.SourceOpt{
		Snapshotter:    bmSnapshotter,
		ContentStore:   opt.ContentStore,
		SessionManager: opt.SessionManager,
		Applier:        opt.Applier,
		CacheAccessor:  cm,
	})
	if err != nil {
		return nil, err
	}

	sm.Register(is)

	gs, err := git.NewSource(git.Opt{
		CacheAccessor: cm,
		MetadataStore: opt.MetadataStore,
	})
	if err != nil {
		return nil, err
	}

	sm.Register(gs)

	hs, err := http.NewSource(http.Opt{
		CacheAccessor: cm,
		MetadataStore: opt.MetadataStore,
	})
	if err != nil {
		return nil, err
	}

	sm.Register(hs)

	ss, err := local.NewSource(local.Opt{
		SessionManager: opt.SessionManager,
		CacheAccessor:  cm,
		MetadataStore:  opt.MetadataStore,
	})
	if err != nil {
		return nil, err
	}
	sm.Register(ss)

	exporters := map[string]exporter.Exporter{}

	imageExporter, err := imageexporter.New(imageexporter.Opt{
		Snapshotter:    bmSnapshotter,
		ContentStore:   opt.ContentStore,
		Differ:         opt.Differ,
		Images:         opt.ImageStore,
		SessionManager: opt.SessionManager,
	})
	if err != nil {
		return nil, err
	}
	exporters[client.ExporterImage] = imageExporter

	localExporter, err := localexporter.New(localexporter.Opt{
		SessionManager: opt.SessionManager,
	})
	if err != nil {
		return nil, err
	}
	exporters[client.ExporterLocal] = localExporter

	ce := cacheimport.NewCacheExporter(cacheimport.ExporterOpt{
		Snapshotter:    bmSnapshotter,
		ContentStore:   opt.ContentStore,
		SessionManager: opt.SessionManager,
		Differ:         opt.Differ,
	})

	ci := cacheimport.NewCacheImporter(cacheimport.ImportOpt{
		Snapshotter:    bmSnapshotter,
		ContentStore:   opt.ContentStore,
		Applier:        opt.Applier,
		CacheAccessor:  cm,
		SessionManager: opt.SessionManager,
	})

	return &Worker{
		WorkerOpt:        opt,
		Snapshotter:      bmSnapshotter,
		CacheManager:     cm,
		SourceManager:    sm,
		InstructionCache: ic,
		Exporters:        exporters,
		ImageSource:      is,
		CacheExporter:    ce,
		CacheImporter:    ci,
	}, nil
}
