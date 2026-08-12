//go:build linux

package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	specs "github.com/opencontainers/runtime-spec/specs-go"
)

func TestMountStubsCleanerForSpec(t *testing.T) {
	root := t.TempDir()
	clean := MountStubsCleanerForSpec(context.Background(), root, []specs.Mount{
		{Destination: "/proc"},
	}, true)

	for _, path := range []string{"proc", "sys"} {
		if err := os.MkdirAll(filepath.Join(root, path), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	clean()

	if _, err := os.Lstat(filepath.Join(root, "proc")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("runtime-created /proc survived cleanup: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "sys")); err != nil {
		t.Fatalf("rootless user-owned /sys was removed: %v", err)
	}
}
