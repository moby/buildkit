package snapshot

import (
	"context"

	"github.com/containerd/containerd/snapshots"
	digest "github.com/opencontainers/go-digest"
)

// Snapshotter defines interface that any snapshot implementation should satisfy
type Snapshotter interface {
	snapshots.Snapshotter
	Blobmapper
}

type Blobmapper interface {
	GetBlob(ctx context.Context, key string) (digest.Digest, digest.Digest, error)
	SetBlob(ctx context.Context, key string, diffID, blob digest.Digest) error
}
