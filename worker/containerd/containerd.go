package containerd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/containerd/containerd"
	"github.com/containerd/containerd/content"
	"github.com/moby/buildkit/cache/metadata"
	"github.com/moby/buildkit/executor/containerdexecutor"
	"github.com/moby/buildkit/worker/base"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
)

// NewWorkerOpt creates a WorkerOpt.
// But it does not set the following fields:
//  - SessionManager
func NewWorkerOpt(root string, address, snapshotterName string, labels map[string]string, opts ...containerd.ClientOpt) (base.WorkerOpt, error) {
	// TODO: take lock to make sure there are no duplicates
	opts = append([]containerd.ClientOpt{containerd.WithDefaultNamespace("buildkit")}, opts...)
	client, err := containerd.New(address, opts...)
	if err != nil {
		return base.WorkerOpt{}, errors.Wrapf(err, "failed to connect client to %q . make sure containerd is running", address)
	}
	return newContainerd(root, client, snapshotterName, labels)
}

func newContainerd(root string, client *containerd.Client, snapshotterName string, labels map[string]string) (base.WorkerOpt, error) {
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
	opt := base.WorkerOpt{
		ID:              id,
		Labels:          xlabels,
		MetadataStore:   md,
		Executor:        containerdexecutor.New(client, root),
		BaseSnapshotter: client.SnapshotService(snapshotterName),
		ContentStore:    &noGCContentStore{client.ContentStore()},
		Applier:         df,
		Differ:          df,
		ImageStore:      client.ImageService(),
	}
	return opt, nil
}

// TODO: Replace this with leases

type noGCContentStore struct {
	content.Store
}
type noGCWriter struct {
	content.Writer
}

func (cs *noGCContentStore) Writer(ctx context.Context, ref string, size int64, expected digest.Digest) (content.Writer, error) {
	w, err := cs.Store.Writer(ctx, ref, size, expected)
	return &noGCWriter{w}, err
}

func (w *noGCWriter) Commit(ctx context.Context, size int64, expected digest.Digest, opts ...content.Opt) error {
	opts = append(opts, func(info *content.Info) error {
		if info.Labels == nil {
			info.Labels = map[string]string{}
		}
		info.Labels["containerd.io/gc.root"] = time.Now().UTC().Format(time.RFC3339Nano)
		return nil
	})
	return w.Writer.Commit(ctx, size, expected, opts...)
}
