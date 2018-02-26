package runc

import (
	"context"
	"os"
	"path/filepath"

	"github.com/boltdb/bolt"
	"github.com/containerd/containerd/content/local"
	"github.com/containerd/containerd/diff/apply"
	"github.com/containerd/containerd/diff/walking"
	ctdmetadata "github.com/containerd/containerd/metadata"
	ctdsnapshot "github.com/containerd/containerd/snapshots"
	"github.com/moby/buildkit/cache/metadata"
	"github.com/moby/buildkit/executor/runcexecutor"
	containerdsnapshot "github.com/moby/buildkit/snapshot/containerd"
	"github.com/moby/buildkit/worker/base"
)

// SnapshotterFactory instantiates a snapshotter
type SnapshotterFactory struct {
	Name string
	New  func(root string) (ctdsnapshot.Snapshotter, error)
}

// NewWorkerOpt creates a WorkerOpt.
// But it does not set the following fields:
//  - SessionManager
func NewWorkerOpt(root string, snFactory SnapshotterFactory, labels map[string]string) (base.WorkerOpt, error) {
	var opt base.WorkerOpt
	name := "runc-" + snFactory.Name
	root = filepath.Join(root, name)
	if err := os.MkdirAll(root, 0700); err != nil {
		return opt, err
	}
	md, err := metadata.NewStore(filepath.Join(root, "metadata.db"))
	if err != nil {
		return opt, err
	}
	exe, err := runcexecutor.New(filepath.Join(root, "executor"))
	if err != nil {
		return opt, err
	}
	s, err := snFactory.New(filepath.Join(root, "snapshots"))
	if err != nil {
		return opt, err
	}

	c, err := local.NewStore(filepath.Join(root, "content"))
	if err != nil {
		return opt, err
	}

	db, err := bolt.Open(filepath.Join(root, "containerdmeta.db"), 0644, nil)
	if err != nil {
		return opt, err
	}

	mdb := ctdmetadata.NewDB(db, c, map[string]ctdsnapshot.Snapshotter{
		snFactory.Name: s,
	})
	if err := mdb.Init(context.TODO()); err != nil {
		return opt, err
	}

	gc := func(ctx context.Context) error {
		_, err := mdb.GarbageCollect(ctx)
		return err
	}

	c = containerdsnapshot.NewContentStore(mdb.ContentStore(), "buildkit", gc)

	id, err := base.ID(root)
	if err != nil {
		return opt, err
	}
	xlabels := base.Labels("oci", snFactory.Name)
	for k, v := range labels {
		xlabels[k] = v
	}
	opt = base.WorkerOpt{
		ID:            id,
		Labels:        xlabels,
		MetadataStore: md,
		Executor:      exe,
		Snapshotter:   containerdsnapshot.NewSnapshotter(mdb.Snapshotter(snFactory.Name), c, md, "buildkit", gc),
		ContentStore:  c,
		Applier:       apply.NewFileSystemApplier(c),
		Differ:        walking.NewWalkingDiff(c),
		ImageStore:    nil, // explicitly
	}
	return opt, nil
}
