//go:build linux

package resources

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
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

// markDelegated sets the extended attribute that systemd sets on delegated
// cgroups.
func markDelegated(t *testing.T, dir string) {
	t.Helper()
	if err := unix.Setxattr(dir, "user.delegate", []byte("1"), 0); err != nil {
		t.Skipf("user extended attributes are not supported here: %v", err)
	}
}

func TestPrepareRootlessCgroup(t *testing.T) {
	const own = "/user.slice/user-1000.slice/user@1000.service/app.slice/buildkit.service"

	t.Run("marked as delegated", func(t *testing.T) {
		root := t.TempDir()
		dir := fakeCgroup(t, root, own, "cpuset cpu io memory pids", "")
		markDelegated(t, dir)

		cg, err := prepareRootlessCgroup(root, own, false)
		require.NoError(t, err)
		require.Equal(t, own, cg.Path)
		require.Equal(t, []string{"cpuset", "cpu", "io", "memory", "pids"}, cg.Controllers)
		require.DirExists(t, filepath.Join(dir, initGroup))
	})

	t.Run("delegated by configuration", func(t *testing.T) {
		root := t.TempDir()
		dir := fakeCgroup(t, root, own, "cpu memory pids", "")

		cg, err := prepareRootlessCgroup(root, own, true)
		require.NoError(t, err)
		require.Equal(t, own, cg.Path)
		require.DirExists(t, filepath.Join(dir, initGroup))
	})

	t.Run("not delegated", func(t *testing.T) {
		// For example the scope of a terminal: writable, but systemd did not
		// hand it over.
		root := t.TempDir()
		dir := fakeCgroup(t, root, own, "cpu memory pids", "")

		_, err := prepareRootlessCgroup(root, own, false)
		require.ErrorContains(t, err, "not marked as delegated")
		require.NoDirExists(t, filepath.Join(dir, initGroup))
	})

	t.Run("missing memory controller", func(t *testing.T) {
		root := t.TempDir()
		dir := fakeCgroup(t, root, own, "cpu pids", "")

		_, err := prepareRootlessCgroup(root, own, true)
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

		_, err := prepareRootlessCgroup(root, own, true)
		require.ErrorContains(t, err, "not writable")
		require.NoDirExists(t, filepath.Join(dir, initGroup))
	})

	t.Run("process outside the PID namespace", func(t *testing.T) {
		root := t.TempDir()
		dir := fakeCgroup(t, root, own, "cpu memory", "")
		require.NoError(t, os.WriteFile(filepath.Join(dir, cgroupProcsFile), []byte("0\n"), 0o644))

		_, err := prepareRootlessCgroup(root, own, true)
		require.ErrorContains(t, err, "outside the PID namespace")
		require.NoDirExists(t, filepath.Join(dir, initGroup))
	})

	t.Run("already in init", func(t *testing.T) {
		root := t.TempDir()
		parent := fakeCgroup(t, root, own, "cpu memory pids", "cpu memory pids")
		markDelegated(t, parent)
		fakeCgroup(t, root, own+"/init", "cpu memory pids", "")

		cg, err := prepareRootlessCgroup(root, own+"/init", false)
		require.NoError(t, err)
		require.Equal(t, own, cg.Path)
		require.Equal(t, []string{"cpu", "memory", "pids"}, cg.Controllers)
	})

	t.Run("missing cgroup", func(t *testing.T) {
		_, err := prepareRootlessCgroup(t.TempDir(), own, true)
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
