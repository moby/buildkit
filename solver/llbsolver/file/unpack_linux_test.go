//go:build linux

package file

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestRootModeFallbackWithoutReadPermission(t *testing.T) {
	for _, kind := range []string{"directory", "fifo", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			dest := t.TempDir()
			name := filepath.Join(dest, "entry")
			switch kind {
			case "directory":
				require.NoError(t, os.Mkdir(name, 0o300))
			case "fifo":
				require.NoError(t, unix.Mkfifo(name, 0o200))
			case "hardlink":
				source := filepath.Join(dest, "source")
				require.NoError(t, os.WriteFile(source, []byte("content"), 0o200))
				require.NoError(t, os.Link(source, name))
			}
			t.Cleanup(func() { require.NoError(t, os.Chmod(name, 0o700)) })
			fd, err := unix.Open(name, unix.O_RDONLY|unix.O_NONBLOCK, 0)
			if err == nil {
				unix.Close(fd)
				t.Skip("read permissions are bypassed; run without CAP_DAC_OVERRIDE/CAP_DAC_READ_SEARCH")
			}
			require.ErrorIs(t, err, unix.EACCES, "fixture must reject the old read-only open")
			root, err := os.OpenRoot(dest)
			require.NoError(t, err)
			defer root.Close()
			parent, err := root.Open(".")
			require.NoError(t, err)
			defer parent.Close()

			// Call the fallback directly so modern kernels also exercise it.
			require.NoError(t, chmodRootFallback(parent, "entry", 0o700|unix.S_ISVTX))
			fi, err := root.Stat("entry")
			require.NoError(t, err)
			require.Equal(t, os.FileMode(0o700), fi.Mode().Perm())
			require.NotZero(t, fi.Mode()&os.ModeSticky)
			if kind == "hardlink" {
				source, err := root.Stat("source")
				require.NoError(t, err)
				require.True(t, os.SameFile(fi, source))
				require.Equal(t, fi.Mode(), source.Mode())
			}
		})
	}
}

func TestRootModeFallbackRejectsSymlinks(t *testing.T) {
	base := t.TempDir()
	dest := filepath.Join(base, "dest")
	require.NoError(t, os.Mkdir(dest, 0o700))
	outside := filepath.Join(base, "outside")
	require.NoError(t, os.WriteFile(outside, []byte("outside"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dest, "inside"), []byte("inside"), 0o600))
	root, err := os.OpenRoot(dest)
	require.NoError(t, err)
	defer root.Close()
	parent, err := root.Open(".")
	require.NoError(t, err)
	defer parent.Close()
	for _, target := range []string{"inside", "../outside", outside, "missing", "link"} {
		t.Run(target, func(t *testing.T) {
			require.NoError(t, root.Symlink(target, "link"))
			defer root.Remove("link")
			require.ErrorIs(t, chmodRootFallback(parent, "link", 0o777), unix.ELOOP)
			for _, name := range []string{outside, filepath.Join(dest, "inside")} {
				fi, err := os.Stat(name)
				require.NoError(t, err)
				require.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
			}
		})
	}
	require.ErrorIs(t, chmodRootFallback(parent, "missing", 0o777), os.ErrNotExist)
}
