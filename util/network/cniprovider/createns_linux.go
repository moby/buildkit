//go:build linux
// +build linux

package cniprovider

import (
	"os"
	"path/filepath"
	"syscall"

	"github.com/containerd/containerd/oci"
	"github.com/cpuguy83/gonso"
	"github.com/moby/buildkit/util/bklog"
	specs "github.com/opencontainers/runtime-spec/specs-go"
	"github.com/pkg/errors"
	"golang.org/x/sys/unix"
)

func cleanOldNamespaces(c *cniProvider) {
	nsDir := filepath.Join(c.root, "net/cni")
	dirEntries, err := os.ReadDir(nsDir)
	if err != nil {
		bklog.L.Debugf("could not read %q for cleanup: %s", nsDir, err)
		return
	}
	go func() {
		for _, d := range dirEntries {
			id := d.Name()
			ns := cniNS{
				id:       id,
				nativeID: filepath.Join(c.root, "net/cni", id),
				handle:   c.CNI,
			}
			if err := ns.release(); err != nil {
				bklog.L.Warningf("failed to release network namespace %q left over from previous run: %s", id, err)
			}
		}
	}()
}

func createNetNS(c *cniProvider, id string) (string, error) {
	nsPath := filepath.Join(c.root, "net/cni", id)
	if err := os.MkdirAll(filepath.Dir(nsPath), 0700); err != nil {
		return "", err
	}

	set, err := gonso.Unshare(unix.CLONE_NEWNET)
	if err != nil {
		return "", errors.Wrap(err, "failed to unshare network namespace")
	}
	defer set.Close()

	if err := set.MountNS(0, nsPath); err != nil {
		return "", errors.Wrap(err, "failed to mount network namespace")
	}
	return nsPath, nil
}

func setNetNS(s *specs.Spec, nsPath string) error {
	return oci.WithLinuxNamespace(specs.LinuxNamespace{
		Type: specs.NetworkNamespace,
		Path: nsPath,
	})(nil, nil, nil, s)
}

func unmountNetNS(nsPath string) error {
	if err := unix.Unmount(nsPath, unix.MNT_DETACH); err != nil {
		if err != syscall.EINVAL && err != syscall.ENOENT {
			return errors.Wrap(err, "error unmounting network namespace")
		}
	}
	return nil
}

func deleteNetNS(nsPath string) error {
	if err := os.Remove(nsPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.Wrapf(err, "error removing network namespace %s", nsPath)
	}
	return nil
}
