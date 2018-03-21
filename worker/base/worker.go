package base

import (
	"context"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"runtime"

	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/diff"
	"github.com/containerd/containerd/images"
	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/cache/cacheimport"
	"github.com/moby/buildkit/cache/instructioncache"
	localcache "github.com/moby/buildkit/cache/instructioncache/local"
	"github.com/moby/buildkit/cache/metadata"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/executor"
	"github.com/moby/buildkit/exporter"
	imageexporter "github.com/moby/buildkit/exporter/containerimage"
	localexporter "github.com/moby/buildkit/exporter/local"
	ociexporter "github.com/moby/buildkit/exporter/oci"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/snapshot"
	"github.com/moby/buildkit/solver"
	"github.com/moby/buildkit/solver/llbop"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/source"
	"github.com/moby/buildkit/source/containerimage"
	"github.com/moby/buildkit/source/git"
	"github.com/moby/buildkit/source/http"
	"github.com/moby/buildkit/source/local"
	"github.com/moby/buildkit/worker"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
)

// TODO: this file should be removed. containerd defines ContainerdWorker, oci defines OCIWorker. There is no base worker.

// WorkerOpt is specific to a worker.
// See also CommonOpt.
type WorkerOpt struct {
	ID             string
	Labels         map[string]string
	SessionManager *session.Manager
	MetadataStore  *metadata.Store
	Executor       executor.Executor
	Snapshotter    snapshot.Snapshotter
	ContentStore   content.Store
	Applier        diff.Applier
	Differ         diff.Comparer
	ImageStore     images.Store // optional
}

// Worker is a local worker instance with dedicated snapshotter, cache, and so on.
// TODO: s/Worker/OpWorker/g ?
type Worker struct {
	WorkerOpt
	CacheManager  cache.Manager
	SourceManager *source.Manager
	Cache         instructioncache.InstructionCache
	Exporters     map[string]exporter.Exporter
	ImageSource   source.Source
	CacheExporter *cacheimport.CacheExporter // TODO: remove
	CacheImporter *cacheimport.CacheImporter // TODO: remove
	// no frontend here
}

// NewWorker instantiates a local worker
func NewWorker(opt WorkerOpt) (*Worker, error) {
	cm, err := cache.NewManager(cache.ManagerOpt{
		Snapshotter:   opt.Snapshotter,
		MetadataStore: opt.MetadataStore,
	})
	if err != nil {
		return nil, err
	}

	ic := &localcache.LocalStore{
		MetadataStore: opt.MetadataStore,
		Cache:         cm,
	}

	sm, err := source.NewManager()
	if err != nil {
		return nil, err
	}

	is, err := containerimage.NewSource(containerimage.SourceOpt{
		Snapshotter:    opt.Snapshotter,
		ContentStore:   opt.ContentStore,
		SessionManager: opt.SessionManager,
		Applier:        opt.Applier,
		ImageStore:     opt.ImageStore,
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

	iw, err := imageexporter.NewImageWriter(imageexporter.WriterOpt{
		Snapshotter:  opt.Snapshotter,
		ContentStore: opt.ContentStore,
		Differ:       opt.Differ,
	})
	if err != nil {
		return nil, err
	}

	imageExporter, err := imageexporter.New(imageexporter.Opt{
		Images:         opt.ImageStore,
		SessionManager: opt.SessionManager,
		ImageWriter:    iw,
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

	ociExporter, err := ociexporter.New(ociexporter.Opt{
		SessionManager: opt.SessionManager,
		ImageWriter:    iw,
		Variant:        ociexporter.VariantOCI,
	})
	if err != nil {
		return nil, err
	}
	exporters[client.ExporterOCI] = ociExporter

	dockerExporter, err := ociexporter.New(ociexporter.Opt{
		SessionManager: opt.SessionManager,
		ImageWriter:    iw,
		Variant:        ociexporter.VariantDocker,
	})
	if err != nil {
		return nil, err
	}
	exporters[client.ExporterDocker] = dockerExporter

	ce := cacheimport.NewCacheExporter(cacheimport.ExporterOpt{
		Snapshotter:    opt.Snapshotter,
		ContentStore:   opt.ContentStore,
		SessionManager: opt.SessionManager,
		Differ:         opt.Differ,
	})

	ci := cacheimport.NewCacheImporter(cacheimport.ImportOpt{
		Snapshotter:    opt.Snapshotter,
		ContentStore:   opt.ContentStore,
		Applier:        opt.Applier,
		CacheAccessor:  cm,
		SessionManager: opt.SessionManager,
	})

	return &Worker{
		WorkerOpt:     opt,
		CacheManager:  cm,
		SourceManager: sm,
		Cache:         ic,
		Exporters:     exporters,
		ImageSource:   is,
		CacheExporter: ce,
		CacheImporter: ci,
	}, nil
}

func (w *Worker) ID() string {
	return w.WorkerOpt.ID
}

func (w *Worker) Labels() map[string]string {
	return w.WorkerOpt.Labels
}

func (w *Worker) ResolveOp(v solver.Vertex, s worker.SubBuilder) (solver.Op, error) {
	switch op := v.Sys().(type) {
	case *pb.Op_Source:
		return llbop.NewSourceOp(v, op, w.SourceManager)
	case *pb.Op_Exec:
		return llbop.NewExecOp(v, op, w.CacheManager, w.Executor)
	case *pb.Op_Build:
		return llbop.NewBuildOp(v, op, s)
	default:
		return nil, errors.Errorf("could not resolve %v", v)
	}
}

func (w *Worker) ResolveImageConfig(ctx context.Context, ref string) (digest.Digest, []byte, error) {
	// ImageSource is typically source/containerimage
	resolveImageConfig, ok := w.ImageSource.(resolveImageConfig)
	if !ok {
		return "", nil, errors.Errorf("worker %q does not implement ResolveImageConfig", w.ID())
	}
	return resolveImageConfig.ResolveImageConfig(ctx, ref)
}

type resolveImageConfig interface {
	ResolveImageConfig(ctx context.Context, ref string) (digest.Digest, []byte, error)
}

func (w *Worker) Exec(ctx context.Context, meta executor.Meta, rootFS cache.ImmutableRef, stdin io.ReadCloser, stdout, stderr io.WriteCloser) error {
	active, err := w.CacheManager.New(ctx, rootFS)
	if err != nil {
		return err
	}
	defer active.Release(context.TODO())
	return w.Executor.Exec(ctx, meta, active, nil, stdin, stdout, stderr)
}

func (w *Worker) DiskUsage(ctx context.Context, opt client.DiskUsageInfo) ([]*client.UsageInfo, error) {
	return w.CacheManager.DiskUsage(ctx, opt)
}

func (w *Worker) Prune(ctx context.Context, ch chan client.UsageInfo) error {
	return w.CacheManager.Prune(ctx, ch)
}

func (w *Worker) Exporter(name string) (exporter.Exporter, error) {
	exp, ok := w.Exporters[name]
	if !ok {
		return nil, errors.Errorf("exporter %q could not be found", name)
	}
	return exp, nil
}

func (w *Worker) InstructionCache() instructioncache.InstructionCache {
	return w.Cache
}

// utility function. could be moved to the constructor logic?
func Labels(executor, snapshotter string) map[string]string {
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "unknown"
	}
	labels := map[string]string{
		worker.LabelOS:          runtime.GOOS,
		worker.LabelArch:        runtime.GOARCH,
		worker.LabelExecutor:    executor,
		worker.LabelSnapshotter: snapshotter,
		worker.LabelHostname:    hostname,
	}
	return labels
}

// ID reads the worker id from the `workerid` file.
// If not exist, it creates a random one,
func ID(root string) (string, error) {
	f := filepath.Join(root, "workerid")
	b, err := ioutil.ReadFile(f)
	if err != nil {
		if os.IsNotExist(err) {
			id := identity.NewID()
			err := ioutil.WriteFile(f, []byte(id), 0400)
			return id, err
		} else {
			return "", err
		}
	}
	return string(b), nil
}
