//go:build !windows

package winlayers

import (
	"context"
	"errors"
	"io"

	"github.com/containerd/containerd/v2/core/mount"
)

func importLinuxLayerAsWindows(_ context.Context, _ string, _ io.Reader, _ []string) (int64, error) {
	return 0, errors.New("importLinuxLayerAsWindows is only supported on Windows")
}

func getParentLayerPaths(_ []mount.Mount) []string {
	return nil
}
