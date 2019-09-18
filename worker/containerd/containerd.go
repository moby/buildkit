package containerd

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/containerd/containerd"
	introspection "github.com/containerd/containerd/api/services/introspection/v1"
	"github.com/containerd/containerd/gc"
	"github.com/containerd/containerd/leases"
	"github.com/moby/buildkit/cache/metadata"
	"github.com/moby/buildkit/executor/containerdexecutor"
	"github.com/moby/buildkit/executor/oci"
	containerdsnapshot "github.com/moby/buildkit/snapshot/containerd"
	"github.com/moby/buildkit/util/leaseutil"
	"github.com/moby/buildkit/util/network/netproviders"
	"github.com/moby/buildkit/util/winlayers"
	"github.com/moby/buildkit/worker/base"
	specs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
)

// NewWorkerOpt creates a WorkerOpt.
func NewWorkerOpt(root string, address, snapshotterName, ns string, labels map[string]string, dns *oci.DNSConfig, nopt netproviders.Opt, opts ...containerd.ClientOpt) (base.WorkerOpt, error) {
	opts = append(opts,
		containerd.WithDefaultNamespace(ns),
		containerd.WithDefaultRuntime("io.containerd.runtime.v1.linux"),
	)
	client, err := containerd.New(address, opts...)
	if err != nil {
		return base.WorkerOpt{}, errors.Wrapf(err, "failed to connect client to %q . make sure containerd is running", address)
	}
	return newContainerd(root, client, snapshotterName, ns, labels, dns, nopt)
}

func newContainerd(root string, client *containerd.Client, snapshotterName, ns string, labels map[string]string, dns *oci.DNSConfig, nopt netproviders.Opt) (base.WorkerOpt, error) {
	if strings.Contains(snapshotterName, "/") {
		return base.WorkerOpt{}, errors.Errorf("bad snapshotter name: %q", snapshotterName)
	}
	name := "containerd-" + snapshotterName
	root = filepath.Join(root, name)
	if err := os.MkdirAll(root, 0700); err != nil {
		return base.WorkerOpt{}, errors.Wrapf(err, "failed to create %s", root)
	}

	md, err := metadata.NewStore(filepath.Join(root, "metadata.db"))
	if err != nil {
		return base.WorkerOpt{}, err
	}
	df := client.DiffService()
	// TODO: should use containerd daemon instance ID (containerd/containerd#1862)?
	id, err := base.ID(root)
	if err != nil {
		return base.WorkerOpt{}, err
	}
	xlabels := base.Labels("containerd", snapshotterName)
	for k, v := range labels {
		xlabels[k] = v
	}

	lm := leaseutil.WithNamespace(client.LeasesService(), ns)

	gc := func(ctx context.Context) (gc.Stats, error) {
		l, err := lm.Create(ctx)
		if err != nil {
			return nil, nil
		}
		return nil, lm.Delete(ctx, leases.Lease{ID: l.ID}, leases.SynchronousDelete)
	}

	cs := containerdsnapshot.NewContentStore(client.ContentStore(), ns)

	resp, err := client.IntrospectionService().Plugins(context.TODO(), &introspection.PluginsRequest{Filters: []string{"type==io.containerd.runtime.v1"}})
	if err != nil {
		return base.WorkerOpt{}, errors.Wrap(err, "failed to list runtime plugin")
	}
	if len(resp.Plugins) == 0 {
		return base.WorkerOpt{}, errors.Wrap(err, "failed to get runtime plugin")
	}

	var platforms []specs.Platform
	for _, plugin := range resp.Plugins {
		for _, p := range plugin.Platforms {
			platforms = append(platforms, specs.Platform{
				OS:           p.OS,
				Architecture: p.Architecture,
				Variant:      p.Variant,
			})
		}
	}

	np, err := netproviders.Providers(nopt)
	if err != nil {
		return base.WorkerOpt{}, err
	}

	opt := base.WorkerOpt{
		ID:             id,
		Labels:         xlabels,
		MetadataStore:  md,
		Executor:       containerdexecutor.New(client, root, "", np, dns),
		Snapshotter:    containerdsnapshot.NewSnapshotter(snapshotterName, client.SnapshotService(snapshotterName), ns, nil),
		ContentStore:   cs,
		Applier:        winlayers.NewFileSystemApplierWithWindows(cs, df),
		Differ:         winlayers.NewWalkingDiffWithWindows(cs, df),
		ImageStore:     client.ImageService(),
		Platforms:      platforms,
		LeaseManager:   lm,
		GarbageCollect: gc,
	}
	return opt, nil
}
