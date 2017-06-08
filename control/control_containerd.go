// +build containerd

package control

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	contentapi "github.com/containerd/containerd/api/services/content"
	diffapi "github.com/containerd/containerd/api/services/diff"
	snapshotapi "github.com/containerd/containerd/api/services/snapshot"
	contentservice "github.com/containerd/containerd/services/content"
	diffservice "github.com/containerd/containerd/services/diff"
	snapshotservice "github.com/containerd/containerd/services/snapshot"
	"github.com/pkg/errors"
	"github.com/tonistiigi/buildkit_poc/worker/runcworker"
	"google.golang.org/grpc"
)

func NewContainerd(root, address string) (*Controller, error) {
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, errors.Wrapf(err, "failed to create %s", root)
	}

	// TODO: take lock to make sure there are no duplicates

	gopts := []grpc.DialOption{
		grpc.WithBlock(),
		grpc.WithInsecure(),
		grpc.WithTimeout(100 * time.Second),
		grpc.WithDialer(dialer),
		grpc.FailOnNonTempDialError(true),
	}
	conn, err := grpc.Dial(dialAddress(address), gopts...)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to connect to %q . make sure containerd is running", address)
	}

	pd, err := newPullDeps(conn)
	if err != nil {
		return nil, err
	}

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

func newPullDeps(conn *grpc.ClientConn) (*pullDeps, error) {
	s := snapshotservice.NewSnapshotterFromClient(snapshotapi.NewSnapshotClient(conn))
	c := contentservice.NewStoreFromClient(contentapi.NewContentClient(conn))
	a := diffservice.NewDiffServiceFromClient(diffapi.NewDiffClient(conn))

	return &pullDeps{
		Snapshotter:  s,
		ContentStore: c,
		Applier:      a,
	}, nil
}

func dialer(address string, timeout time.Duration) (net.Conn, error) {
	address = strings.TrimPrefix(address, "unix://")
	return net.DialTimeout("unix", address, timeout)
}

func dialAddress(address string) string {
	return fmt.Sprintf("unix://%s", address)
}
