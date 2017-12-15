package containerd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/containerd/containerd"
	"github.com/containerd/containerd/namespaces"
	"github.com/containerd/containerd/snapshots"
	"github.com/moby/buildkit/cache/metadata"
	"github.com/moby/buildkit/executor/containerdexecutor"
	"github.com/moby/buildkit/identity"
	containerdsnapshot "github.com/moby/buildkit/snapshot/containerd"
	"github.com/moby/buildkit/util/network"
	"github.com/moby/buildkit/worker/base"
	"github.com/pkg/errors"
)

// NewWorkerOpt creates a WorkerOpt.
// But it does not set the following fields:
//  - SessionManager
func NewWorkerOpt(root string, address, snapshotterName string, labels map[string]string, bridge string, opts ...containerd.ClientOpt) (base.WorkerOpt, error) {
	// TODO: take lock to make sure there are no duplicates
	opts = append([]containerd.ClientOpt{containerd.WithDefaultNamespace("buildkit")}, opts...)
	client, err := containerd.New(address, opts...)
	if err != nil {
		return base.WorkerOpt{}, errors.Wrapf(err, "failed to connect client to %q . make sure containerd is running", address)
	}
	return newContainerd(root, client, snapshotterName, labels, bridge)
}

func newContainerd(root string, client *containerd.Client, snapshotterName string, labels map[string]string, bridge string) (base.WorkerOpt, error) {
	if strings.Contains(snapshotterName, "/") {
		return base.WorkerOpt{}, errors.Errorf("bad snapshotter name: %q", snapshotterName)
	}

	bridgeProvider, _ := network.InitBridgeProvider(bridge)

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

	gc := func(ctx context.Context) error {
		// TODO: how to avoid this?
		snapshotter := client.SnapshotService(snapshotterName)
		ctx = namespaces.WithNamespace(ctx, "buildkit")
		key := identity.NewID()
		if _, err := snapshotter.Prepare(ctx, key, "", snapshots.WithLabels(map[string]string{
			"containerd.io/gc.root": time.Now().UTC().Format(time.RFC3339Nano),
		})); err != nil {
			return err
		}
		if err := snapshotter.Remove(ctx, key); err != nil {
			return err
		}
		return nil
	}

	cs := containerdsnapshot.NewContentStore(client.ContentStore(), "buildkit", gc)

	opt := base.WorkerOpt{
		ID:            id,
		Labels:        xlabels,
		MetadataStore: md,
		Executor:      containerdexecutor.New(client, root, bridgeProvider),
		Snapshotter:   containerdsnapshot.NewSnapshotter(client.SnapshotService(snapshotterName), cs, md, "buildkit", gc),
		ContentStore:  cs,
		Applier:       df,
		Differ:        df,
		ImageStore:    client.ImageService(),
	}
	return opt, nil
}
