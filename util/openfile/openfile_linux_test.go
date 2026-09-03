package openfile

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

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

// A device whose open observably fails tells us whether the driver was entered
// at all: opening /dev/tty without a controlling terminal returns ENXIO. If the
// helper reports ErrNotRegular instead, the inode was never opened.
func TestRegularInRootDoesNotEnterDriver(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "tty")
	if err := unix.Mknod(p, unix.S_IFCHR|0600, int(unix.Mkdev(5, 0))); err != nil {
		t.Skipf("cannot create device node: %v", err)
	}

	f, err := os.OpenFile(p, os.O_RDONLY|unix.O_NONBLOCK, 0)
	if err == nil {
		f.Close()
		t.Skip("this host opens /dev/tty successfully, the check would not be conclusive")
	}
	require.ErrorIs(t, err, unix.ENXIO, "expected a plain open to enter the driver")

	_, err = RegularInRoot(dir, "tty")
	require.ErrorIs(t, err, ErrNotRegular)
	require.NotErrorIs(t, err, unix.ENXIO, "the driver was entered")
}
