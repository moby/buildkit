//go:build linux

package resources

import (
	"os"
	"path/filepath"
	"strconv"

	"golang.org/x/sys/unix"
)

func isCgroup2() bool {
	var st unix.Statfs_t
	err := unix.Statfs(defaultMountpoint, &st)
	if err != nil {
		return false
	}
	return st.Type == unix.CGROUP2_SUPER_MAGIC
}

// prepareCgroupControllers moves the processes in the root cgroup into an
// "init" child and enables all controllers for the children, when
// BUILDKIT_SETUP_CGROUPV2_ROOT is set. It is meant for buildkitd running in a
// container with its own cgroup namespace.
func prepareCgroupControllers() error {
	v, ok := os.LookupEnv("BUILDKIT_SETUP_CGROUPV2_ROOT")
	if !ok {
		return nil
	}
	if b, _ := strconv.ParseBool(v); !b {
		return nil
	}
	leaf := filepath.Join(defaultMountpoint, initGroup)
	if err := os.MkdirAll(leaf, 0o755); err != nil {
		return err
	}
	if err := moveProcs(defaultMountpoint, leaf); err != nil {
		return err
	}
	available, err := readControllers(filepath.Join(defaultMountpoint, cgroupControllersFile))
	if err != nil {
		return err
	}
	_, err = enableControllers(defaultMountpoint, available, nil)
	return err
}
