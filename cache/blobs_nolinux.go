// +build !linux

package cache

import (
	"context"
	"fmt"

	"github.com/containerd/containerd/mount"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

func (sr *immutableRef) computeOverlayBlob(ctx context.Context, lower, upper []mount.Mount, mediaType string, ref string) (_ ocispec.Descriptor, err error) {
	return ocispec.Descriptor{}, fmt.Errorf("overlayfs-based diff computing is unsupported")
}
