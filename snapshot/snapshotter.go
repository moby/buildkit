package snapshot

import (
	"context"

	"github.com/containerd/containerd/mount"
	"github.com/containerd/containerd/snapshots"
	"github.com/docker/docker/pkg/idtools"
	"github.com/pkg/errors"
)

type Mountable interface {
	Mount() ([]mount.Mount, func() error, error)
	IdentityMapping() *idtools.IdentityMapping
}

// Snapshotter defines interface that any snapshot implementation should satisfy
type Snapshotter interface {
	Name() string
	Mounts(ctx context.Context, key string) (Mountable, error)
	Prepare(ctx context.Context, key, parent string, opts ...snapshots.Opt) error
	View(ctx context.Context, key, parent string, opts ...snapshots.Opt) error

	Stat(ctx context.Context, key string) (snapshots.Info, error)
	Update(ctx context.Context, info snapshots.Info, fieldpaths ...string) (snapshots.Info, error)
	Usage(ctx context.Context, key string) (snapshots.Usage, error)
	Commit(ctx context.Context, name, key string, opts ...snapshots.Opt) error
	Walk(ctx context.Context, fn snapshots.WalkFunc, filters ...string) error
	Close() error
	IdentityMapping() *idtools.IdentityMapping
	// Containerd returns the underlying containerd snapshotter interface used by this snapshotter
	Containerd() snapshots.Snapshotter
}

func FromContainerdSnapshotter(
	name string,
	s snapshots.Snapshotter,
	idmap *idtools.IdentityMapping,
) Snapshotter {
	return &fromContainerd{name: name, snapshotter: s, idmap: idmap}
}

type fromContainerd struct {
	name        string
	snapshotter snapshots.Snapshotter
	idmap       *idtools.IdentityMapping
}

func (sn *fromContainerd) Name() string {
	return sn.name
}

func (sn *fromContainerd) IdentityMapping() *idtools.IdentityMapping {
	return sn.idmap
}

func (sn *fromContainerd) Close() error {
	return sn.snapshotter.Close()
}

func (sn *fromContainerd) Containerd() snapshots.Snapshotter {
	return sn.snapshotter
}

func (sn *fromContainerd) Stat(ctx context.Context, key string) (snapshots.Info, error) {
	return sn.snapshotter.Stat(ctx, key)
}

func (sn *fromContainerd) Usage(ctx context.Context, key string) (snapshots.Usage, error) {
	return sn.snapshotter.Usage(ctx, key)
}

func (sn *fromContainerd) Mounts(ctx context.Context, key string) (_ Mountable, rerr error) {
	mounts, err := sn.snapshotter.Mounts(ctx, key)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get mounts")
	}
	return &staticMountable{mounts: mounts, idmap: sn.idmap, id: key}, nil
}

func (sn *fromContainerd) Prepare(ctx context.Context, key, parent string, opts ...snapshots.Opt) error {
	_, err := sn.snapshotter.Prepare(ctx, key, parent, opts...)
	return err
}

func (sn *fromContainerd) View(ctx context.Context, key, parent string, opts ...snapshots.Opt) error {
	_, err := sn.snapshotter.View(ctx, key, parent, opts...)
	return err
}

func (sn *fromContainerd) Commit(ctx context.Context, name, key string, opts ...snapshots.Opt) error {
	return sn.snapshotter.Commit(ctx, name, key, opts...)
}

func (sn *fromContainerd) Update(ctx context.Context, info snapshots.Info, fieldpaths ...string) (snapshots.Info, error) {
	return sn.snapshotter.Update(ctx, info, fieldpaths...)
}

func (sn *fromContainerd) Walk(ctx context.Context, fn snapshots.WalkFunc, filters ...string) error {
	return sn.snapshotter.Walk(ctx, fn, filters...)
}
