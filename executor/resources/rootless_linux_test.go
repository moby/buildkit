//go:build linux

package resources

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// fakeCgroup creates the files of a cgroup v2 directory under root. The files
// are plain files, so writing to cgroup.procs does not move anything.
func fakeCgroup(t *testing.T, root, cgroup, controllers, subtree string) string {
	t.Helper()
	dir := filepath.Join(root, cgroup)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, cgroupControllersFile), []byte(controllers+"\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, cgroupSubtreeFile), []byte(subtree+"\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, cgroupProcsFile), nil, 0o644))
	return dir
}

func TestPrepareRootlessCgroup(t *testing.T) {
	const own = "/user.slice/user-1000.slice/user@1000.service/app.slice/buildkit.service"

	t.Run("delegated", func(t *testing.T) {
		root := t.TempDir()
		dir := fakeCgroup(t, root, own, "cpuset cpu io memory pids", "")

		cg, err := prepareRootlessCgroup(root, own)
		require.NoError(t, err)
		require.Equal(t, own, cg.Path)
		require.Equal(t, []string{"cpuset", "cpu", "io", "memory", "pids"}, cg.Controllers)
		require.DirExists(t, filepath.Join(dir, initGroup))
	})

	t.Run("missing memory controller", func(t *testing.T) {
		root := t.TempDir()
		dir := fakeCgroup(t, root, own, "cpu pids", "")

		_, err := prepareRootlessCgroup(root, own)
		require.ErrorContains(t, err, "needs")
		require.NoDirExists(t, filepath.Join(dir, initGroup))
	})

	t.Run("read-only", func(t *testing.T) {
		if os.Getuid() == 0 {
			t.Skip("root can write to read-only files")
		}
		root := t.TempDir()
		dir := fakeCgroup(t, root, own, "cpu memory", "")
		require.NoError(t, os.Chmod(filepath.Join(dir, cgroupProcsFile), 0o444))

		_, err := prepareRootlessCgroup(root, own)
		require.ErrorContains(t, err, "not writable")
		require.NoDirExists(t, filepath.Join(dir, initGroup))
	})

	t.Run("already in init", func(t *testing.T) {
		root := t.TempDir()
		fakeCgroup(t, root, own, "cpu memory pids", "cpu memory pids")
		fakeCgroup(t, root, own+"/init", "cpu memory pids", "")

		cg, err := prepareRootlessCgroup(root, own+"/init")
		require.NoError(t, err)
		require.Equal(t, own, cg.Path)
		require.Equal(t, []string{"cpu", "memory", "pids"}, cg.Controllers)
	})

	t.Run("init without prepared parent", func(t *testing.T) {
		root := t.TempDir()
		fakeCgroup(t, root, own, "cpu memory", "")
		fakeCgroup(t, root, own+"/init", "cpu memory", "")

		cg, err := prepareRootlessCgroup(root, own+"/init")
		require.NoError(t, err)
		require.Equal(t, own+"/init", cg.Path)
	})

	t.Run("missing cgroup", func(t *testing.T) {
		_, err := prepareRootlessCgroup(t.TempDir(), own)
		require.Error(t, err)
	})
}

func TestOwnCgroup(t *testing.T) {
	f := filepath.Join(t.TempDir(), "cgroup")

	require.NoError(t, os.WriteFile(f, []byte("0::/user.slice/buildkit.service\n"), 0o644))
	p, err := ownCgroup(f)
	require.NoError(t, err)
	require.Equal(t, "/user.slice/buildkit.service", p)

	// cgroup v1 only
	require.NoError(t, os.WriteFile(f, []byte("12:memory:/user.slice\n1:name=systemd:/user.slice\n"), 0o644))
	_, err = ownCgroup(f)
	require.ErrorContains(t, err, "no cgroup v2 entry")

	require.NoError(t, os.WriteFile(f, []byte("0::/user.slice/../../etc\n"), 0o644))
	_, err = ownCgroup(f)
	require.ErrorContains(t, err, "unexpected cgroup path")
}
