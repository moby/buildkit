//go:build !windows

package executor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCreateMountStubs(t *testing.T) {
	for _, tc := range []struct {
		name  string
		dests []string
	}{
		{name: "parent first", dests: []string{"/sys", "/sys/fs/cgroup"}},
		{name: "child first", dests: []string{"/sys/fs/cgroup", "/sys"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			require.NoError(t, CreateMountStubs(root, tc.dests, os.Getuid(), os.Getgid()))

			st, err := os.Stat(filepath.Join(root, "sys"))
			require.NoError(t, err)
			require.True(t, st.IsDir())

			// The runtime creates this one inside the /sys mount, so it must not end
			// up in the rootfs.
			_, err = os.Lstat(filepath.Join(root, "sys/fs"))
			require.ErrorIs(t, err, os.ErrNotExist)
		})
	}

	t.Run("keeps existing directory", func(t *testing.T) {
		root := t.TempDir()
		require.NoError(t, os.Mkdir(filepath.Join(root, "sys"), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(root, "sys/keep"), []byte("keep"), 0o644))

		require.NoError(t, CreateMountStubs(root, []string{"/sys"}, os.Getuid(), os.Getgid()))

		st, err := os.Stat(filepath.Join(root, "sys"))
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o700), st.Mode().Perm())
		require.FileExists(t, filepath.Join(root, "sys/keep"))
	})
}
