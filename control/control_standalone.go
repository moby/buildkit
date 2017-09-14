// +build standalone

package control

import (
	"context"
	"os"
	"path/filepath"

	"github.com/containerd/containerd/content/local"
	"github.com/containerd/containerd/differ"
	"github.com/containerd/containerd/mount"
	"github.com/containerd/containerd/namespaces"
	ctdsnapshot "github.com/containerd/containerd/snapshot"
	"github.com/containerd/containerd/snapshot/overlay"
	"github.com/moby/buildkit/worker/runcworker"
	"github.com/pkg/errors"
)

func NewStandalone(root string) (*Controller, error) {
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, errors.Wrapf(err, "failed to create %s", root)
	}

	// TODO: take lock to make sure there are no duplicates

	pd, err := newStandalonePullDeps(root)
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

func newStandalonePullDeps(root string) (*pullDeps, error) {
	s, err := overlay.NewSnapshotter(filepath.Join(root, "snapshots"))
	if err != nil {
		return nil, err
	}

	c, err := local.NewStore(filepath.Join(root, "content"))
	if err != nil {
		return nil, err
	}

	df, err := differ.NewWalkingDiff(c)
	if err != nil {
		return nil, err
	}

	return &pullDeps{
		Snapshotter:  &nsSnapshotter{s},
		ContentStore: c,
		Applier:      df,
		Differ:       df,
	}, nil
}

// this should be supported by containerd. currently packages are unusable without wrapping
const dummyNs = "buildkit"

type nsSnapshotter struct {
	ctdsnapshot.Snapshotter
}

func (s *nsSnapshotter) Stat(ctx context.Context, key string) (ctdsnapshot.Info, error) {
	ctx = namespaces.WithNamespace(ctx, dummyNs)
	return s.Snapshotter.Stat(ctx, key)
}

func (s *nsSnapshotter) Update(ctx context.Context, info ctdsnapshot.Info, fieldpaths ...string) (ctdsnapshot.Info, error) {
	ctx = namespaces.WithNamespace(ctx, dummyNs)
	return s.Snapshotter.Update(ctx, info, fieldpaths...)
}

func (s *nsSnapshotter) Usage(ctx context.Context, key string) (ctdsnapshot.Usage, error) {
	ctx = namespaces.WithNamespace(ctx, dummyNs)
	return s.Snapshotter.Usage(ctx, key)
}
func (s *nsSnapshotter) Mounts(ctx context.Context, key string) ([]mount.Mount, error) {
	ctx = namespaces.WithNamespace(ctx, dummyNs)
	return s.Snapshotter.Mounts(ctx, key)
}
func (s *nsSnapshotter) Prepare(ctx context.Context, key, parent string, opts ...ctdsnapshot.Opt) ([]mount.Mount, error) {
	ctx = namespaces.WithNamespace(ctx, dummyNs)
	return s.Snapshotter.Prepare(ctx, key, parent, opts...)
}
func (s *nsSnapshotter) View(ctx context.Context, key, parent string, opts ...ctdsnapshot.Opt) ([]mount.Mount, error) {
	ctx = namespaces.WithNamespace(ctx, dummyNs)
	return s.Snapshotter.View(ctx, key, parent, opts...)
}
func (s *nsSnapshotter) Commit(ctx context.Context, name, key string, opts ...ctdsnapshot.Opt) error {
	ctx = namespaces.WithNamespace(ctx, dummyNs)
	return s.Snapshotter.Commit(ctx, name, key, opts...)
}
func (s *nsSnapshotter) Remove(ctx context.Context, key string) error {
	ctx = namespaces.WithNamespace(ctx, dummyNs)
	return s.Snapshotter.Remove(ctx, key)
}
func (s *nsSnapshotter) Walk(ctx context.Context, fn func(context.Context, ctdsnapshot.Info) error) error {
	ctx = namespaces.WithNamespace(ctx, dummyNs)
	return s.Snapshotter.Walk(ctx, fn)
}
