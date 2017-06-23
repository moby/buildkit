// +build containerd

package control

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/containerd/containerd"
	"github.com/moby/buildkit/worker/runcworker"
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

	w, err := runcworker.New(filepath.Join(root, "runc"))
	if err != nil {
		return nil, err
	}

	opt.Worker = w

	return NewController(*opt)
}

func newContainerdPullDeps(client *containerd.Client) *pullDeps {
	return &pullDeps{
		Snapshotter:  client.SnapshotService(),
		ContentStore: client.ContentStore(),
		Applier:      client.DiffService(),
	}
}

func dialer(address string, timeout time.Duration) (net.Conn, error) {
	address = strings.TrimPrefix(address, "unix://")
	return net.DialTimeout("unix", address, timeout)
}

func dialAddress(address string) string {
	return fmt.Sprintf("unix://%s", address)
}
