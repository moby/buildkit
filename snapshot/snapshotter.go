package snapshot

import (
	"context"

	"github.com/containerd/containerd/mount"
	"github.com/containerd/containerd/snapshot"
)

// Snapshotter defines interface that any snapshot implementation should satisfy
type Snapshotter interface {
	Stat(ctx context.Context, key string) (snapshot.Info, error)
	Prepare(ctx context.Context, key, parent string) ([]mount.Mount, error)
	Mounts(ctx context.Context, key string) ([]mount.Mount, error)
	Commit(ctx context.Context, name, key string) error
	View(ctx context.Context, key, parent string) ([]mount.Mount, error)
	Remove(ctx context.Context, key string) error
}
