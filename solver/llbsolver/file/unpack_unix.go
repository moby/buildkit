//go:build !windows

package file

import "github.com/containerd/containerd/v2/pkg/archive"

func unpackPlatformApplyOpts() []archive.ApplyOpt {
	return nil
}
