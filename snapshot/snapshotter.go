package snapshot

import (
	"github.com/containerd/containerd/snapshot"
)

// Snapshotter defines interface that any snapshot implementation should satisfy
type Snapshotter interface {
	snapshot.Snapshotter
}
