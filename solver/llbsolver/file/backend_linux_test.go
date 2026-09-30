//go:build linux

package file

import (
	"testing"

	"golang.org/x/sys/unix"
)

func TestMkfileReplacesDeviceNode(t *testing.T) {
	requireMkfileReplaces(t, func(t *testing.T, p string) {
		// /dev/null, so that a regression writes somewhere harmless
		if err := unix.Mknod(p, unix.S_IFCHR|0600, int(unix.Mkdev(1, 3))); err != nil {
			t.Skipf("cannot create device node: %v", err)
		}
	})
}
