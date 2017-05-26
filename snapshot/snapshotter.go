package snapshot

import (
	"context"

	"github.com/containerd/containerd/mount"
)

// Snapshotter defines interface that any snapshot implementation should satisfy
type Snapshotter interface {
	Prepare(ctx context.Context, key, parent string) ([]mount.Mount, error)
	Mounts(ctx context.Context, key string) ([]mount.Mount, error)
	Commit(ctx context.Context, name, key string) error
}
