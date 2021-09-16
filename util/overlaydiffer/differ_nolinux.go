//go:build !linux
// +build !linux

package overlaydiffer

import (
	"context"

	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/diff"
	"github.com/containerd/containerd/mount"
	"github.com/moby/buildkit/snapshot"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
)

func NewOverlayDiffer(store content.Store, sn snapshot.Snapshotter, d diff.Comparer) diff.Comparer {
	return &overlayDiffer{
		d: d,
	}
}

type overlayDiffer struct {
	d diff.Comparer
}

func (d *overlayDiffer) Compare(ctx context.Context, lower, upper []mount.Mount, opts ...diff.Opt) (desc ocispecs.Descriptor, err error) {
	return d.d.Compare(ctx, lower, upper, opts...)
}
