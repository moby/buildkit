//go:build linux

package executor

import (
	"context"

	specs "github.com/opencontainers/runtime-spec/specs-go"
)

// MountStubsCleanerForSpec cleans stubs for mounts in a finalized OCI spec.
func MountStubsCleanerForSpec(ctx context.Context, dir string, mounts []specs.Mount, recursive bool) func() {
	cleanupMounts := make([]Mount, len(mounts))
	for i, m := range mounts {
		cleanupMounts[i].Dest = m.Destination
	}
	return MountStubsCleaner(ctx, dir, cleanupMounts, recursive)
}
