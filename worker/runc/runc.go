//go:build linux

package runc

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"strconv"

	"github.com/containerd/containerd/v2/core/diff/apply"
	ctdmetadata "github.com/containerd/containerd/v2/core/metadata"
	ctdsnapshot "github.com/containerd/containerd/v2/core/snapshots"
	"github.com/containerd/containerd/v2/plugins/content/local"
	"github.com/containerd/containerd/v2/plugins/diff/walking"
	"github.com/containerd/platforms"
	"github.com/moby/buildkit/cache/metadata"
	"github.com/moby/buildkit/executor/oci"
	"github.com/moby/buildkit/executor/resources"
	"github.com/moby/buildkit/executor/runcexecutor"
	containerdsnapshot "github.com/moby/buildkit/snapshot/containerd"
	"github.com/moby/buildkit/solver/llbsolver/cdidevices"
	"github.com/moby/buildkit/util/leaseutil"
	"github.com/moby/buildkit/util/network/netproviders"
	"github.com/moby/buildkit/util/winlayers"
	"github.com/moby/buildkit/worker/base"
	wlabel "github.com/moby/buildkit/worker/label"
	"github.com/moby/sys/user"
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
func NewWorkerOpt(root string, snFactory SnapshotterFactory, rootless bool, processMode oci.ProcessMode, labels map[string]string, idmap *user.IdentityMapping, nopt netproviders.Opt, dns *oci.DNSConfig, binary, apparmorProfile string, selinux bool, parallelismSem *semaphore.Weighted, traceSocket, defaultCgroupParent string, cdiManager *cdidevices.Manager) (base.WorkerOpt, error) {
	var opt base.WorkerOpt
	name := "runc-" + snFactory.Name
	root = filepath.Join(root, name)
	if err := os.MkdirAll(root, 0700); err != nil {
		return opt, err
	}

	np, npResolvedMode, err := netproviders.Providers(nopt)
	if err != nil {
		return opt, err
	}

	// Check if user has specified OCI worker binary; if they have, append it to cmds
	var cmds []string
	if binary != "" {
		cmds = append(cmds, binary)
	}

	rm, err := resources.NewMonitor()
	if err != nil {
		return opt, err
	}

	exe, err := runcexecutor.New(runcexecutor.Opt{
		// Root directory
		Root: filepath.Join(root, "executor"),
		// If user has specified OCI worker binary, it will be sent to the runc executor to find and use
		// Otherwise, a nil array will be sent and the default OCI worker binary will be used
		CommandCandidates: cmds,
		// without root privileges
		Rootless:            rootless,
		ProcessMode:         processMode,
		IdentityMapping:     idmap,
		DNS:                 dns,
		ApparmorProfile:     apparmorProfile,
		SELinux:             selinux,
		TracingSocket:       traceSocket,
		DefaultCgroupParent: defaultCgroupParent,
		ResourceMonitor:     rm,
		CDIManager:          cdiManager,
	}, np)
	if err != nil {
		return opt, err
	}
	s, err := snFactory.New(filepath.Join(root, "snapshots"))
	if err != nil {
		return opt, err
	}

	localstore, err := local.NewStore(filepath.Join(root, "content"))
	if err != nil {
		return opt, err
	}

	db, err := bolt.Open(filepath.Join(root, "containerdmeta.db"), 0644, nil)
	if err != nil {
		return opt, err
	}

	mdb := ctdmetadata.NewDB(db, localstore, map[string]ctdsnapshot.Snapshotter{
		snFactory.Name: s,
	})
	if err := mdb.Init(context.TODO()); err != nil {
		return opt, err
	}

	c := containerdsnapshot.NewContentStore(mdb.ContentStore(), "buildkit")

	id, err := base.ID(root)
	if err != nil {
		return opt, err
	}
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "unknown"
	}
	xlabels := map[string]string{
		wlabel.Executor:       "oci",
		wlabel.Snapshotter:    snFactory.Name,
		wlabel.Hostname:       hostname,
		wlabel.Network:        npResolvedMode,
		wlabel.OCIProcessMode: processMode.String(),
		wlabel.SELinuxEnabled: strconv.FormatBool(selinux),
	}
	if apparmorProfile != "" {
		xlabels[wlabel.ApparmorProfile] = apparmorProfile
	}

	maps.Copy(xlabels, labels)

	md, err := metadata.NewStore(filepath.Join(root, "metadata_v2.db"))
	if err != nil {
		return opt, err
	}

	opt = base.WorkerOpt{
		ID:               id,
		Root:             root,
		Labels:           xlabels,
		MetadataStore:    md,
		NetworkProviders: np,
		Executor:         exe,
		Snapshotter:      containerdsnapshot.NewSnapshotter(snFactory.Name, mdb.Snapshotter(snFactory.Name), "buildkit", idmap),
		ContentStore:     c,
		Applier:          winlayers.NewFileSystemApplierWithWindows(c, apply.NewFileSystemApplier(c)),
		Differ:           winlayers.NewWalkingDiffWithWindows(c, walking.NewWalkingDiff(c)),
		ImageStore:       nil, // explicitly
		Platforms:        []ocispecs.Platform{platforms.Normalize(platforms.DefaultSpec())},
		IdentityMapping:  idmap,
		LeaseManager:     leaseutil.WithNamespace(ctdmetadata.NewLeaseManager(mdb), "buildkit"),
		GarbageCollect:   mdb.GarbageCollect,
		ParallelismSem:   parallelismSem,
		MountPoolRoot:    filepath.Join(root, "cachemounts"),
		ResourceMonitor:  rm,
		CDIManager:       cdiManager,
	}
	return opt, nil
}
