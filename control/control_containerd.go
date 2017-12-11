// +build containerd

package control

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/containerd/containerd"
	"github.com/containerd/containerd/content"
	"github.com/moby/buildkit/worker/containerdworker"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
)

func NewContainerd(root, address string) (*Controller, error) {
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, errors.Wrapf(err, "failed to create %s", root)
	}

	// TODO: take lock to make sure there are no duplicates
	client, err := containerd.New(address, containerd.WithDefaultNamespace("buildkit"))
	if err != nil {
		return nil, errors.Wrapf(err, "failed to connect client to %q . make sure containerd is running", address)
	}

	pd := newContainerdPullDeps(client)

	opt, err := defaultControllerOpts(root, *pd)
	if err != nil {
		return nil, err
	}

	opt.Worker = containerdworker.New(client, root)

	return NewController(*opt)
}

func newContainerdPullDeps(client *containerd.Client) *pullDeps {
	diff := client.DiffService()
	return &pullDeps{
		Snapshotter:  client.SnapshotService(containerd.DefaultSnapshotter),
		ContentStore: &noGCContentStore{client.ContentStore()},
		Applier:      diff,
		Differ:       diff,
		Images:       client.ImageService(),
	}
}

func dialer(address string, timeout time.Duration) (net.Conn, error) {
	address = strings.TrimPrefix(address, "unix://")
	return net.DialTimeout("unix", address, timeout)
}

func dialAddress(address string) string {
	return fmt.Sprintf("unix://%s", address)
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
