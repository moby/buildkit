//go:build linux

package resources

import (
	"bufio"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/pkg/errors"
	"golang.org/x/sys/unix"
)

// controllers that a rootless cgroup must have for it to be used. Without
// them, the memory and CPU limits a build step asks for cannot be applied.
var rootlessRequiredControllers = []string{"cpu", "memory"}

// RootlessCgroup is a cgroup v2 tree that a rootless buildkitd can write to.
type RootlessCgroup struct {
	// Path is the cgroup path relative to the cgroupfs root (/sys/fs/cgroup),
	// e.g. "/user.slice/user-1000.slice/user@1000.service/app.slice/buildkit.service".
	// Build steps go into child cgroups of it.
	Path string
	// Controllers lists the controllers enabled for the child cgroups.
	Controllers []string
}

// PrepareRootlessCgroup checks whether the cgroup buildkitd runs in has been
// delegated to it, which is how a rootless process gets a cgroup v2 tree that
// it can manage. If so, it moves the buildkitd processes into a leaf cgroup
// named "init" and enables the controllers for the other children, so that
// build steps can get their own cgroups with resource limits.
//
// If buildkitd already runs in an "init" cgroup whose parent has the required
// controllers enabled, for example after `rootlesskit --evacuate-cgroup2=init`,
// the parent is used as it is.
//
// It returns an error, and changes nothing, when cgroup v2 is not mounted at
// /sys/fs/cgroup, the cpu or memory controller is missing, or the cgroup is not
// writable. That is the case for a cgroup that was not delegated and for a
// read-only /sys/fs/cgroup, as in an unprivileged Kubernetes pod.
func PrepareRootlessCgroup() (*RootlessCgroup, error) {
	if !isCgroup2() {
		return nil, errors.Errorf("cgroup v2 is not mounted at %s", defaultMountpoint)
	}
	own, err := ownCgroup("/proc/self/cgroup")
	if err != nil {
		return nil, err
	}
	return prepareRootlessCgroup(defaultMountpoint, own)
}

func prepareRootlessCgroup(mountpoint, own string) (*RootlessCgroup, error) {
	if path.Base(own) == initGroup {
		parent := path.Dir(own)
		parentDir := filepath.Join(mountpoint, parent)
		if enabled, err := readControllers(filepath.Join(parentDir, cgroupSubtreeFile)); err == nil &&
			hasControllers(enabled, rootlessRequiredControllers) && isWritable(parentDir) == nil {
			return &RootlessCgroup{Path: parent, Controllers: enabled}, nil
		}
	}

	dir := filepath.Join(mountpoint, own)
	available, err := readControllers(filepath.Join(dir, cgroupControllersFile))
	if err != nil {
		return nil, err
	}
	if !hasControllers(available, rootlessRequiredControllers) {
		return nil, errors.Errorf("cgroup %s has controllers %q, needs %q", own, available, rootlessRequiredControllers)
	}
	if err := isWritable(dir); err != nil {
		return nil, err
	}

	if err := moveProcs(dir, filepath.Join(dir, initGroup)); err != nil {
		return nil, err
	}
	var enabled []string
	for _, c := range available {
		if err := os.WriteFile(filepath.Join(dir, cgroupSubtreeFile), []byte("+"+c), 0); err != nil {
			if slices.Contains(rootlessRequiredControllers, c) {
				return nil, errors.Wrapf(err, "failed to enable cgroup controller %q in %s", c, own)
			}
			continue
		}
		enabled = append(enabled, c)
	}
	return &RootlessCgroup{Path: own, Controllers: enabled}, nil
}

// ownCgroup returns the cgroup v2 path of the current process, as found in
// the "0::<path>" line of /proc/self/cgroup.
func ownCgroup(procSelfCgroup string) (string, error) {
	f, err := os.Open(procSelfCgroup)
	if err != nil {
		return "", errors.WithStack(err)
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		if p, ok := strings.CutPrefix(s.Text(), "0::"); ok {
			if !path.IsAbs(p) || path.Clean(p) != p {
				return "", errors.Errorf("unexpected cgroup path %q in %s", p, procSelfCgroup)
			}
			return p, nil
		}
	}
	if err := s.Err(); err != nil {
		return "", errors.WithStack(err)
	}
	return "", errors.Errorf("no cgroup v2 entry in %s", procSelfCgroup)
}

// isWritable checks that the current user can create child cgroups in dir and
// move processes and enable controllers there.
func isWritable(dir string) error {
	for _, p := range []string{dir, filepath.Join(dir, cgroupProcsFile), filepath.Join(dir, cgroupSubtreeFile)} {
		if err := unix.Access(p, unix.W_OK); err != nil {
			return errors.Wrapf(err, "%s is not writable", p)
		}
	}
	return nil
}

func readControllers(file string) ([]string, error) {
	dt, err := os.ReadFile(file)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	return strings.Fields(string(dt)), nil
}

func hasControllers(have, want []string) bool {
	for _, c := range want {
		if !slices.Contains(have, c) {
			return false
		}
	}
	return true
}

// moveProcs moves every process in the cgroup dir into the cgroup to, which is
// created if needed. A cgroup v2 can only enable controllers for its children
// while it has no processes of its own.
func moveProcs(dir, to string) error {
	if err := os.MkdirAll(to, 0o755); err != nil {
		return errors.WithStack(err)
	}
	// New processes can show up while moving, so read the list again until it
	// is empty.
	for range 10 {
		dt, err := os.ReadFile(filepath.Join(dir, cgroupProcsFile))
		if err != nil {
			return errors.WithStack(err)
		}
		pids := strings.Fields(string(dt))
		if len(pids) == 0 {
			return nil
		}
		for _, pid := range pids {
			//nolint:gosec // to is under the cgroup of this process, see ownCgroup
			if err := os.WriteFile(filepath.Join(to, cgroupProcsFile), []byte(pid), 0); err != nil {
				return errors.Wrapf(err, "failed to move process %s to %s", pid, to)
			}
		}
	}
	return errors.Errorf("processes keep appearing in %s", dir)
}
