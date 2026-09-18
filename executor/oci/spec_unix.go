//go:build !windows

package oci

import "github.com/containerd/containerd/v2/core/mount"

// no effect for non-Windows
func normalizeMountType(mType string) string {
	return mType
}

// isNamedPipeMount is always false on non-Windows platforms.
func isNamedPipeMount(_ mount.Mount) bool {
	return false
}
