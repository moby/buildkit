package network

import (
	"os"
	"path/filepath"
	"syscall"

	"github.com/containerd/containerd/oci"
	"github.com/containerd/go-cni"
	"github.com/gofrs/flock"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/util/network/netns_create"
	specs "github.com/opencontainers/runtime-spec/specs-go"
	"github.com/pkg/errors"
	"golang.org/x/sys/unix"
)

func NewCNIProvider(opt Opt) (Provider, error) {
	if _, err := os.Stat(opt.CNIConfigPath); err != nil {
		return nil, errors.Wrapf(err, "failed to read cni config %q", opt.CNIConfigPath)
	}
	if _, err := os.Stat(opt.CNIBinaryDir); err != nil {
		return nil, errors.Wrapf(err, "failed to read cni binary dir %q", opt.CNIBinaryDir)
	}

	cniHandle, err := cni.New(
		cni.WithMinNetworkCount(2),
		cni.WithConfFile(opt.CNIConfigPath),
		cni.WithPluginDir([]string{opt.CNIBinaryDir}),
		cni.WithLoNetwork,
		cni.WithInterfacePrefix(("eth")))
	if err != nil {
		return nil, err
	}

	if err != nil {
		return nil, err
	}

	cp := &cniProvider{CNI: cniHandle, root: opt.Root}
	if err := cp.initNetwork(); err != nil {
		return nil, err
	}
	return cp, nil
}

type cniProvider struct {
	cni.CNI
	root string
}

func (c *cniProvider) initNetwork() error {
	if v := os.Getenv("BUILDKIT_CNI_INIT_LOCK_PATH"); v != "" {
		l := flock.New(v)
		if err := l.Lock(); err != nil {
			return err
		}
		defer l.Unlock()
	}
	ns, err := c.New()
	if err != nil {
		return err
	}
	return ns.Close()
}

func (c *cniProvider) New() (Namespace, error) {
	id := identity.NewID()
	nsPath := filepath.Join(c.root, "net/cni", id)
	if err := os.MkdirAll(filepath.Dir(nsPath), 0700); err != nil {
		return nil, err
	}

	if err := netns_create.CreateNetNS(nsPath); err != nil {
		return nil, err
	}

	if _, err := c.CNI.Setup(id, nsPath); err != nil {
		return nil, errors.Wrap(err, "CNI setup error")
	}

	return &cniNS{path: nsPath, id: id, handle: c.CNI}, nil
}

type cniNS struct {
	handle cni.CNI
	id     string
	path   string
}

func (ns *cniNS) Set(s *specs.Spec) {
	oci.WithLinuxNamespace(specs.LinuxNamespace{
		Type: specs.NetworkNamespace,
		Path: ns.path,
	})(nil, nil, nil, s)
}

func (ns *cniNS) Close() error {
	err := ns.handle.Remove(ns.id, ns.path)

	if err1 := unix.Unmount(ns.path, unix.MNT_DETACH); err1 != nil {
		if err1 != syscall.EINVAL && err1 != syscall.ENOENT && err == nil {
			err = errors.Wrap(err1, "error unmounting network namespace")
		}
	}
	if err1 := os.RemoveAll(filepath.Dir(ns.path)); err1 != nil && !os.IsNotExist(err1) && err == nil {
		err = errors.Wrap(err, "error removing network namespace")
	}

	return err
}
