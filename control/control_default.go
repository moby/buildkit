// +build standalone containerd

package control

import (
	"path/filepath"

	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/images"
	"github.com/containerd/containerd/rootfs"
	ctdsnapshot "github.com/containerd/containerd/snapshot"
	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/cache/instructioncache"
	"github.com/moby/buildkit/cache/metadata"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/exporter"
	imageexporter "github.com/moby/buildkit/exporter/containerimage"
	localexporter "github.com/moby/buildkit/exporter/local"
	"github.com/moby/buildkit/frontend"
	"github.com/moby/buildkit/frontend/dockerfile"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/snapshot/blobmapping"
	"github.com/moby/buildkit/source"
	"github.com/moby/buildkit/source/containerimage"
	"github.com/moby/buildkit/source/git"
	"github.com/moby/buildkit/source/local"
)

type pullDeps struct {
	Snapshotter  ctdsnapshot.Snapshotter
	ContentStore content.Store
	Applier      rootfs.Applier
	Differ       rootfs.MountDiffer
	Images       images.Store
}

func defaultControllerOpts(root string, pd pullDeps) (*Opt, error) {
	md, err := metadata.NewStore(filepath.Join(root, "metadata.db"))
	if err != nil {
		return nil, err
	}

	snapshotter, err := blobmapping.NewSnapshotter(blobmapping.Opt{
		Content:       pd.ContentStore,
		Snapshotter:   pd.Snapshotter,
		MetadataStore: md,
	})
	if err != nil {
		return nil, err
	}

	cm, err := cache.NewManager(cache.ManagerOpt{
		Snapshotter:   snapshotter,
		MetadataStore: md,
	})
	if err != nil {
		return nil, err
	}

	ic := &instructioncache.LocalStore{
		MetadataStore: md,
		Cache:         cm,
	}

	sm, err := source.NewManager()
	if err != nil {
		return nil, err
	}

	is, err := containerimage.NewSource(containerimage.SourceOpt{
		Snapshotter:   snapshotter,
		ContentStore:  pd.ContentStore,
		Applier:       pd.Applier,
		CacheAccessor: cm,
	})
	if err != nil {
		return nil, err
	}

	sm.Register(is)

	gs, err := git.NewSource(git.Opt{
		CacheAccessor: cm,
		MetadataStore: md,
	})
	if err != nil {
		return nil, err
	}

	sm.Register(gs)

	sessm, err := session.NewManager()
	if err != nil {
		return nil, err
	}

	ss, err := local.NewSource(local.Opt{
		SessionManager: sessm,
		CacheAccessor:  cm,
		MetadataStore:  md,
	})
	if err != nil {
		return nil, err
	}
	sm.Register(ss)

	exporters := map[string]exporter.Exporter{}

	imageExporter, err := imageexporter.New(imageexporter.Opt{
		Snapshotter:   snapshotter,
		ContentStore:  pd.ContentStore,
		Differ:        pd.Differ,
		CacheAccessor: cm,
		Images:        pd.Images,
	})
	if err != nil {
		return nil, err
	}
	exporters[client.ExporterImage] = imageExporter

	localExporter, err := localexporter.New(localexporter.Opt{
		SessionManager: sessm,
	})
	if err != nil {
		return nil, err
	}
	exporters[client.ExporterLocal] = localExporter

	frontends := map[string]frontend.Frontend{}
	frontends["dockerfile.v0"] = dockerfile.NewDockerfileFrontend()

	return &Opt{
		Snapshotter:      snapshotter,
		CacheManager:     cm,
		SourceManager:    sm,
		InstructionCache: ic,
		Exporters:        exporters,
		SessionManager:   sessm,
		Frontends:        frontends,
	}, nil
}
