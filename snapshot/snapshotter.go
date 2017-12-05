package snapshot

import (
	"github.com/containerd/containerd/snapshots"
)

// Snapshotter defines interface that any snapshot implementation should satisfy
type Snapshotter interface {
	snapshots.Snapshotter
}
