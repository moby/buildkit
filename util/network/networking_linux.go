package network

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	cni "github.com/containerd/go-cni"
	"github.com/moby/buildkit/identity"
	"github.com/sirupsen/logrus"
	"golang.org/x/sys/unix"
)

// CNIProvider implements Provider
type CNIProvider struct {
	CNIHandle   cni.CNI
	networkOpts NetworkOpts
}

// InitProvider instantiate CNIProvider object
func InitProvider(networkOpts NetworkOpts) (Provider, error) {
	return &CNIProvider{networkOpts: networkOpts}, nil
}

//NewInterface creates CNI interface
func (p CNIProvider) NewInterface() (Interface, error) {
	if p.networkOpts.Type == "host" {
		return nil, nil
	}

	cniHandle, err := InitCNI(p.networkOpts.CNIConfigPath, p.networkOpts.CNIPluginPath)
	if err != nil {
		logrus.Errorf("CNI network error : %v", err)
		return nil, err
	}

	iface := CNIInterface{CNIHandle: cniHandle}

	return &iface, nil
}

// Release resources
func (p CNIProvider) Release(Interface) error {
	return nil
}

//CNIInterface spaceholder for functions
type CNIInterface struct {
	CNIHandle cni.CNI
	id        string
	nspath    string
}

//Set network namespace of task & Setup CNI interface
func (v *CNIInterface) Set(pid int) error {
	v.id = getRandomName()

	processNSPath := fmt.Sprintf("/proc/%d/ns/net", pid)
	nsPath := fmt.Sprintf("/var/run/netns/cni-%s", v.id)

	if err := os.MkdirAll(filepath.Dir(nsPath), 0711); err != nil {
		return fmt.Errorf("cannot create %s : %v", filepath.Dir(nsPath), err)
	}

	// Test if file exist and can be open
	mountPointFd, err := os.Create(nsPath)
	if err != nil {
		return fmt.Errorf("cannot open %s : %v", nsPath, err)
	}
	mountPointFd.Close()

	// Bind mount processNSPath to nsPath
	if err := unix.Mount(processNSPath, nsPath, "none", unix.MS_BIND, ""); err != nil {
		return fmt.Errorf("cannot mount %s:%s : %v", processNSPath, nsPath, err)
	}

	_, err = v.CNIHandle.Setup(v.id, nsPath)
	if err != nil {
		return fmt.Errorf("CNI setup error : %v", err)
	}

	v.nspath = nsPath
	return nil
}

//Remove the link from system
func (v *CNIInterface) Remove(pid int) error {

	err := v.CNIHandle.Remove(v.id, v.nspath)
	if err != nil {
		logrus.Error("CNI network error : ", err)
	}

	// unconditional try to unmount/remove the namespace as a separate
	// process from the one that creates the namespace,
	// and Remove() will only do that if it is the same process.
	if err = unix.Unmount(v.nspath, unix.MNT_DETACH); err != nil {
		if err != syscall.EINVAL && err != syscall.ENOENT {
			logrus.Error("error unmounting network namespace : %v ", err)
		}
	}
	if err = os.RemoveAll(v.nspath); err != nil && !os.IsNotExist(err) {
		logrus.Error("error removing network namespace  :%v ", err)
	}
	return err
}

func getRandomName() string {
	//max length of Network Interface name.
	return identity.NewID()[:15]
}
