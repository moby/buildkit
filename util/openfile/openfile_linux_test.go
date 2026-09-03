package openfile

import (
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestRejectsDeviceNode(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "special")
	// /dev/null, so that a regression reads somewhere harmless
	if err := unix.Mknod(p, unix.S_IFCHR|0600, int(unix.Mkdev(1, 3))); err != nil {
		t.Skipf("cannot create device node: %v", err)
	}
	requireRejected(t, p)
}
