package cniprovider

import (
	"os"
	"path/filepath"
	"syscall"

	"github.com/containerd/containerd/oci"
	"github.com/containerd/go-cni"
	"github.com/gofrs/flock"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/util/network"
	specs "github.com/opencontainers/runtime-spec/specs-go"
	"github.com/pkg/errors"
	"golang.org/x/sys/unix"
)

type Opt struct {
	Root       string
	ConfigPath string
	BinaryDir  string
}

func New(opt Opt) (network.Provider, error) {
	if _, err := os.Stat(opt.ConfigPath); err != nil {
		return nil, errors.Wrapf(err, "failed to read cni config %q", opt.ConfigPath)
	}
	if _, err := os.Stat(opt.BinaryDir); err != nil {
		return nil, errors.Wrapf(err, "failed to read cni binary dir %q", opt.BinaryDir)
	}

	cniHandle, err := cni.New(
		cni.WithMinNetworkCount(2),
		cni.WithConfFile(opt.ConfigPath),
		cni.WithPluginDir([]string{opt.BinaryDir}),
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

func (c *cniProvider) New() (network.Namespace, error) {
	id := identity.NewID()
	nsPath := filepath.Join(c.root, "net/cni", id)
	if err := os.MkdirAll(filepath.Dir(nsPath), 0700); err != nil {
		return nil, err
	}

	if err := createNetNS(nsPath); err != nil {
		os.RemoveAll(filepath.Dir(nsPath))
		return nil, err
	}

	if _, err := c.CNI.Setup(id, nsPath); err != nil {
		os.RemoveAll(filepath.Dir(nsPath))
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
