//go:build linux

package resources

import (
	"bufio"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/moby/buildkit/util/bklog"
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

// PrepareRootlessCgroup sets up the cgroup that buildkitd runs in so that
// build steps can get their own child cgroups with resource limits. It moves
// the buildkitd processes into a leaf child named "init" and enables the
// controllers for the other children.
//
// It only takes over a cgroup that was delegated to buildkitd. The cgroup
// counts as delegated when systemd marked it with the user.delegate or
// trusted.delegate extended attribute, or when assumeDelegated is set because
// the user configured it. systemd marks the cgroups that the system manager
// delegates, for example a system service with Delegate=yes. A unit of the
// per-user systemd instance has no such mark even with Delegate=yes, and it
// looks the same as the cgroup of a terminal, so for those the user has to set
// assumeDelegated.
//
// If buildkitd already runs in an "init" cgroup with a delegated parent, for
// example in a systemd unit with DelegateSubgroup=init or after
// `rootlesskit --evacuate-cgroup2=init`, the parent is used and only the
// controllers it is missing get enabled.
//
// On error the cgroup is left as it was. Errors are expected for cgroup v1, a
// read-only /sys/fs/cgroup as in an unprivileged Kubernetes pod, and a cgroup
// that is not delegated.
func PrepareRootlessCgroup(assumeDelegated bool) (*RootlessCgroup, error) {
	own, err := ownCgroup2()
	if err != nil {
		return nil, err
	}
	return prepareRootlessCgroup(defaultMountpoint, own, assumeDelegated)
}

func prepareRootlessCgroup(mountpoint, own string, assumeDelegated bool) (*RootlessCgroup, error) {
	if path.Base(own) == initGroup {
		if cg, err := useInitParent(mountpoint, path.Dir(own), assumeDelegated); err == nil {
			return cg, nil
		}
	}

	dir := filepath.Join(mountpoint, own)
	if !assumeDelegated && !isDelegated(dir) {
		return nil, errors.Errorf("cgroup %s is not marked as delegated by systemd; set rootlessDelegatedCgroup if it is", own)
	}
	available, err := availableControllers(mountpoint, own)
	if err != nil {
		return nil, err
	}
	if err := isWritable(dir); err != nil {
		return nil, err
	}

	leaf := filepath.Join(dir, initGroup)
	createdLeaf, err := mkdirIfMissing(leaf)
	if err != nil {
		return nil, err
	}
	undo := func() {
		// Best effort, so that a failed setup leaves the cgroup as it was.
		moveProcs(leaf, dir)
		if createdLeaf {
			os.Remove(leaf)
		}
	}
	if err := moveProcs(dir, leaf); err != nil {
		undo()
		return nil, err
	}
	enabled, err := enableControllers(dir, available, rootlessRequiredControllers)
	if err != nil {
		undo()
		return nil, err
	}
	// systemd starts the helper commands of a unit, like ExecReload= and
	// ExecStop=, in the top cgroup of the unit. That fails once the cgroup
	// has controllers enabled for its children. With DelegateSubgroup=init
	// systemd starts everything in the init child instead, and buildkitd
	// uses the parent as it is.
	bklog.L.Infof("rootless: moved the processes of cgroup %s into %s/%s. If buildkitd runs as a systemd service, set DelegateSubgroup=%s in its unit so that systemd can still start its helper commands", own, own, initGroup, initGroup)
	return &RootlessCgroup{Path: own, Controllers: enabled}, nil
}

// useInitParent uses parent, the parent of the "init" cgroup that buildkitd
// runs in, for the build steps. This is the layout that systemd creates for a
// unit with DelegateSubgroup=init, and that rootlesskit creates with
// --evacuate-cgroup2=init. The processes of the unit are already in init, so
// the controllers that are not enabled yet can be enabled right away.
func useInitParent(mountpoint, parent string, assumeDelegated bool) (*RootlessCgroup, error) {
	dir := filepath.Join(mountpoint, parent)
	if !assumeDelegated && !isDelegated(dir) {
		return nil, errors.Errorf("cgroup %s is not marked as delegated by systemd", parent)
	}
	if err := isWritable(dir); err != nil {
		return nil, err
	}
	available, err := availableControllers(mountpoint, parent)
	if err != nil {
		return nil, err
	}
	enabled, err := readControllers(filepath.Join(dir, cgroupSubtreeFile))
	if err != nil {
		return nil, err
	}
	var missing []string
	for _, c := range available {
		if !slices.Contains(enabled, c) {
			missing = append(missing, c)
		}
	}
	added, err := enableControllers(dir, missing, rootlessRequiredControllers)
	if err != nil {
		return nil, err
	}
	return &RootlessCgroup{Path: parent, Controllers: append(enabled, added...)}, nil
}

// isDelegated reports whether systemd marked the cgroup dir as delegated.
// systemd sets user.delegate (readable by the owner) and trusted.delegate
// (readable by root) to "1" on cgroups it delegates.
func isDelegated(dir string) bool {
	for _, attr := range []string{"user.delegate", "trusted.delegate"} {
		buf := make([]byte, 8)
		n, err := unix.Getxattr(dir, attr, buf)
		if err == nil && string(buf[:n]) == "1" {
			return true
		}
	}
	return false
}

// mkdirIfMissing creates dir and reports whether it had to.
func mkdirIfMissing(dir string) (bool, error) {
	err := os.Mkdir(dir, 0o755)
	if errors.Is(err, os.ErrExist) {
		return false, nil
	}
	if err != nil {
		return false, errors.WithStack(err)
	}
	return true, nil
}

// enableControllers enables the controllers in available for the children of
// the cgroup dir and returns the ones it enabled. When one of required cannot
// be enabled, it disables the ones it enabled before and returns an error.
// The other controllers are skipped with a warning.
func enableControllers(dir string, available, required []string) ([]string, error) {
	var enabled []string
	for _, c := range available {
		if err := os.WriteFile(filepath.Join(dir, cgroupSubtreeFile), []byte("+"+c), 0); err != nil {
			if slices.Contains(required, c) {
				disableControllers(dir, enabled)
				return nil, errors.Wrapf(err, "failed to enable cgroup controller %q in %s", c, dir)
			}
			bklog.L.Warnf("failed to enable cgroup controller %q in %s: %v", c, dir, err)
			continue
		}
		enabled = append(enabled, c)
	}
	return enabled, nil
}

func disableControllers(dir string, controllers []string) {
	for _, c := range slices.Backward(controllers) {
		os.WriteFile(filepath.Join(dir, cgroupSubtreeFile), []byte("-"+c), 0)
	}
}

// OwnCgroupControllers returns the cgroup v2 controllers available to the
// cgroup that buildkitd runs in. Under a user's systemd, the other units of
// that user get the same controllers, so this tells which limits rootless
// runc can apply in a systemd slice of the same user. It returns an error when
// the cpu or memory controller is missing.
func OwnCgroupControllers() ([]string, error) {
	own, err := ownCgroup2()
	if err != nil {
		return nil, err
	}
	return availableControllers(defaultMountpoint, own)
}

// ownCgroup2 returns the cgroup v2 path of the current process.
func ownCgroup2() (string, error) {
	if !isCgroup2() {
		return "", errors.Errorf("cgroup v2 is not mounted at %s", defaultMountpoint)
	}
	return ownCgroup("/proc/self/cgroup")
}

// availableControllers returns the controllers that the cgroup cg under
// mountpoint can enable for its children. It returns an error when the cpu or
// memory controller is missing.
func availableControllers(mountpoint, cg string) ([]string, error) {
	available, err := readControllers(filepath.Join(mountpoint, cg, cgroupControllersFile))
	if err != nil {
		return nil, err
	}
	if !hasControllers(available, rootlessRequiredControllers) {
		return nil, errors.Errorf("cgroup %s has controllers %q, needs %q", cg, available, rootlessRequiredControllers)
	}
	return available, nil
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

// moveProcs moves every process in the cgroup dir into the cgroup to. A
// cgroup v2 can only enable controllers for its children while it has no
// processes of its own.
func moveProcs(dir, to string) error {
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
		// cgroup.procs lists processes outside our PID namespace as 0, for
		// example the rootlesskit parent when rootlesskit runs with --pidns.
		// They can't be moved from here, and writing 0 would move this
		// process instead.
		if slices.Contains(pids, "0") {
			return errors.Errorf("%s has processes outside the PID namespace of buildkitd", dir)
		}
		for _, pid := range pids {
			//nolint:gosec // to is under the cgroup of this process, see ownCgroup
			err := os.WriteFile(filepath.Join(to, cgroupProcsFile), []byte(pid), 0)
			if errors.Is(err, unix.ESRCH) {
				continue // the process exited in the meantime
			}
			if err != nil {
				return errors.Wrapf(err, "failed to move process %s to %s", pid, to)
			}
		}
	}
	return errors.Errorf("processes keep appearing in %s", dir)
}
