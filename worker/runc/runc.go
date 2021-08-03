package runc

import (
	"context"
	"os"
	"path/filepath"

	"github.com/containerd/containerd/content/local"
	"github.com/containerd/containerd/diff/apply"
	"github.com/containerd/containerd/diff/walking"
	ctdmetadata "github.com/containerd/containerd/metadata"
	"github.com/containerd/containerd/platforms"
	ctdsnapshot "github.com/containerd/containerd/snapshots"
	"github.com/docker/docker/pkg/idtools"
	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/cache/metadata"
	"github.com/moby/buildkit/executor/oci"
	"github.com/moby/buildkit/executor/runcexecutor"
	containerdsnapshot "github.com/moby/buildkit/snapshot/containerd"
	"github.com/moby/buildkit/util/leaseutil"
	"github.com/moby/buildkit/util/network/netproviders"
	"github.com/moby/buildkit/util/winlayers"
	"github.com/moby/buildkit/worker/base"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	bolt "go.etcd.io/bbolt"
	"golang.org/x/sync/semaphore"
)

// SnapshotterFactory instantiates a snapshotter
type SnapshotterFactory struct {
	Name string
	New  func(root string) (ctdsnapshot.Snapshotter, error)
}

// NewWorkerOpt creates a WorkerOpt.
func NewWorkerOpt(ctx context.Context, root string, snFactory SnapshotterFactory, rootless bool, processMode oci.ProcessMode, labels map[string]string, idmap *idtools.IdentityMapping, nopt netproviders.Opt, dns *oci.DNSConfig, binary, apparmorProfile string, parallelismSem *semaphore.Weighted, traceSocket string) (base.WorkerOpt, error) {
	var opt base.WorkerOpt
	name := "runc-" + snFactory.Name
	root = filepath.Join(root, name)
	if err := os.MkdirAll(root, 0700); err != nil {
		return opt, err
	}

	np, err := netproviders.Providers(nopt)
	if err != nil {
		return opt, err
	}

	// Check if user has specified OCI worker binary; if they have, append it to cmds
	var cmds []string
	if binary != "" {
		cmds = append(cmds, binary)
	}

	exe, err := runcexecutor.New(runcexecutor.Opt{
		// Root directory
		Root: filepath.Join(root, "executor"),
		// If user has specified OCI worker binary, it will be sent to the runc executor to find and use
		// Otherwise, a nil array will be sent and the default OCI worker binary will be used
		CommandCandidates: cmds,
		// without root privileges
		Rootless:        rootless,
		ProcessMode:     processMode,
		IdentityMapping: idmap,
		DNS:             dns,
		ApparmorProfile: apparmorProfile,
		TracingSocket:   traceSocket,
	}, np)
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

	c = containerdsnapshot.NewContentStore(mdb.ContentStore(), "buildkit")

	id, err := base.ID(root)
	if err != nil {
		return opt, err
	}
	xlabels := base.Labels("oci", snFactory.Name)
	for k, v := range labels {
		xlabels[k] = v
	}
	lm := leaseutil.WithNamespace(ctdmetadata.NewLeaseManager(mdb), "buildkit")
	applier := winlayers.NewFileSystemApplierWithWindows(c, apply.NewFileSystemApplier(c))
	differ := winlayers.NewWalkingDiffWithWindows(c, walking.NewWalkingDiff(c))
	snap := containerdsnapshot.NewSnapshotter(snFactory.Name, mdb.Snapshotter(snFactory.Name), "buildkit", idmap)

	if err := cache.MigrateV2(
		context.TODO(),
		filepath.Join(root, "metadata.db"),
		filepath.Join(root, "metadata_v2.db"),
		c,
		snap,
		lm,
	); err != nil {
		return opt, err
	}

	md, err := metadata.NewStore(filepath.Join(root, "metadata_v2.db"))
	if err != nil {
		return opt, err
	}

	opt = base.WorkerOpt{
		ID:              id,
		Labels:          xlabels,
		MetadataStore:   md,
		Executor:        exe,
		Snapshotter:     snap,
		ContentStore:    c,
		Applier:         applier,
		Differ:          differ,
		ImageStore:      nil, // explicitly
		Platforms:       []ocispecs.Platform{platforms.Normalize(platforms.DefaultSpec())},
		IdentityMapping: idmap,
		LeaseManager:    lm,
		GarbageCollect:  mdb.GarbageCollect,
		ParallelismSem:  parallelismSem,
	}
	return opt, nil
}
