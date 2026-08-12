//go:build windows

package file

import "github.com/containerd/containerd/v2/pkg/archive"

func unpackPlatformApplyOpts() []archive.ApplyOpt {
	// Windows does not support lchown. The old go-archive path skipped ownership
	// changes on Windows while still extracting the archive contents.
	return []archive.ApplyOpt{archive.WithNoSameOwner()}
}
