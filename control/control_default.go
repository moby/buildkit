// +build standalone containerd

package control

import (
	"path/filepath"

	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/rootfs"
	ctdsnapshot "github.com/containerd/containerd/snapshot"
	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/snapshot/blobmapping"
	"github.com/moby/buildkit/source"
	"github.com/moby/buildkit/source/containerimage"
)

type pullDeps struct {
	Snapshotter  ctdsnapshot.Snapshotter
	ContentStore content.Store
	Applier      rootfs.Applier
}

func defaultControllerOpts(root string, pd pullDeps) (*Opt, error) {
	snapshotter, err := blobmapping.NewSnapshotter(blobmapping.Opt{
		Root:        filepath.Join(root, "blobmap"),
		Content:     pd.ContentStore,
		Snapshotter: pd.Snapshotter,
	})
	if err != nil {
		return nil, err
	}

	cm, err := cache.NewManager(cache.ManagerOpt{
		Snapshotter: snapshotter,
		Root:        filepath.Join(root, "cachemanager"),
	})
	if err != nil {
		return nil, err
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

	return &Opt{
		Snapshotter:   snapshotter,
		CacheManager:  cm,
		SourceManager: sm,
	}, nil
}
