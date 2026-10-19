//go:build linux

package workers

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/moby/buildkit/identity"
	"github.com/pkg/errors"
	"golang.org/x/sys/unix"
)

const cgroupMountpoint = "/sys/fs/cgroup"

// delegateCgroup creates a cgroup v2 owned by uid and gid, the way systemd
// delegates a cgroup to a rootless user service, so that a rootless buildkitd
// started in it can apply per-step resource limits. It returns an empty dir
// when the test environment has no cgroup v2 with the cpu and memory
// controllers available, and the daemon then runs without a delegated cgroup.
func delegateCgroup(uid, gid int) (dir string, cleanup func() error, err error) {
	var st unix.Statfs_t
	if err := unix.Statfs(cgroupMountpoint, &st); err != nil || st.Type != unix.CGROUP2_SUPER_MAGIC {
		return "", nil, nil
	}
	dt, err := os.ReadFile(filepath.Join(cgroupMountpoint, "cgroup.subtree_control"))
	if err != nil {
		return "", nil, nil
	}
	enabled := strings.Fields(string(dt))
	if !slices.Contains(enabled, "cpu") || !slices.Contains(enabled, "memory") {
		return "", nil, nil
	}

	dir = filepath.Join(cgroupMountpoint, "bktest-rootless-"+identity.NewID())
	if err := os.Mkdir(dir, 0o755); err != nil {
		return "", nil, errors.WithStack(err)
	}
	cleanup = func() error {
		return removeCgroup(dir)
	}
	// These are the files that systemd hands over for Delegate=yes.
	for _, p := range []string{"", "cgroup.procs", "cgroup.subtree_control", "cgroup.threads"} {
		if err := os.Chown(filepath.Join(dir, p), uid, gid); err != nil {
			cleanup()
			return "", nil, errors.WithStack(err)
		}
	}
	// systemd marks delegated cgroups like this, and buildkitd only takes
	// over a cgroup with this mark unless configured otherwise.
	if err := unix.Setxattr(dir, "user.delegate", []byte("1"), 0); err != nil {
		cleanup()
		return "", nil, errors.WithStack(err)
	}
	return dir, cleanup, nil
}

// removeCgroup removes dir and the child cgroups that buildkitd created in it.
// A cgroup can only be removed once its children are gone, so it removes the
// deepest ones first.
func removeCgroup(dir string) error {
	var dirs []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			dirs = append(dirs, p)
		}
		return nil
	})
	if err != nil {
		return errors.WithStack(err)
	}
	for _, d := range slices.Backward(dirs) {
		if err := removeEmptyCgroup(d); err != nil {
			return err
		}
	}
	return nil
}

// removeEmptyCgroup removes the cgroup dir. Right after the daemon stopped,
// the kernel can still count exiting processes in it and return EBUSY, so it
// tries again for a few seconds.
func removeEmptyCgroup(dir string) error {
	var err error
	for range 50 {
		err = os.Remove(dir)
		if !errors.Is(err, unix.EBUSY) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	return errors.WithStack(err)
}
