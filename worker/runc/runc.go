package runc

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/containerd/containerd/content/local"
	"github.com/containerd/containerd/diff/apply"
	"github.com/containerd/containerd/diff/walking"
	ctdmetadata "github.com/containerd/containerd/metadata"
	"github.com/containerd/containerd/platforms"
	ctdsnapshot "github.com/containerd/containerd/snapshots"
	"github.com/docker/docker/pkg/idtools"
	"github.com/moby/buildkit/cache/metadata"
	"github.com/moby/buildkit/executor/oci"
	"github.com/moby/buildkit/executor/runcexecutor"
	containerdsnapshot "github.com/moby/buildkit/snapshot/containerd"
	"github.com/moby/buildkit/util/leaseutil"
	"github.com/moby/buildkit/util/network"
	"github.com/moby/buildkit/util/throttle"
	"github.com/moby/buildkit/util/winlayers"
	"github.com/moby/buildkit/worker/base"
	specs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/sirupsen/logrus"
	bolt "go.etcd.io/bbolt"
)

// SnapshotterFactory instantiates a snapshotter
type SnapshotterFactory struct {
	Name string
	New  func(root string) (ctdsnapshot.Snapshotter, error)
}

// NewWorkerOpt creates a WorkerOpt.
func NewWorkerOpt(root string, snFactory SnapshotterFactory, rootless bool, processMode oci.ProcessMode, labels map[string]string, idmap *idtools.IdentityMapping, dns *oci.DNSConfig) (base.WorkerOpt, error) {
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
	exe, err := runcexecutor.New(runcexecutor.Opt{
		// Root directory
		Root: filepath.Join(root, "executor"),
		// without root privileges
		Rootless:        rootless,
		ProcessMode:     processMode,
		IdentityMapping: idmap,
		DNS:             dns,
	}, network.Default())
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

	throttledGC := throttle.Throttle(time.Second, func() {
		if _, err := mdb.GarbageCollect(context.TODO()); err != nil {
			logrus.Errorf("GC error: %+v", err)
		}
	})

	gc := func(ctx context.Context) error {
		throttledGC()
		return nil
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
		ID:              id,
		Labels:          xlabels,
		MetadataStore:   md,
		Executor:        exe,
		Snapshotter:     containerdsnapshot.NewSnapshotter(snFactory.Name, mdb.Snapshotter(snFactory.Name), c, md, "buildkit", gc, idmap),
		ContentStore:    c,
		Applier:         winlayers.NewFileSystemApplierWithWindows(c, apply.NewFileSystemApplier(c)),
		Differ:          winlayers.NewWalkingDiffWithWindows(c, walking.NewWalkingDiff(c)),
		ImageStore:      nil, // explicitly
		Platforms:       []specs.Platform{platforms.Normalize(platforms.DefaultSpec())},
		IdentityMapping: idmap,
		LeaseManager:    leaseutil.WithNamespace(leaseutil.NewManager(mdb), "buildkit"),
	}
	return opt, nil
}
