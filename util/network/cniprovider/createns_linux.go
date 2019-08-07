// +build linux

package cniprovider

import (
	"os"
	"syscall"
	"unsafe"

	"github.com/pkg/errors"
	"golang.org/x/sys/unix"
)

func createNetNS(p string) error {
	f, err := os.Create(p)
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	procNetNSBytes, err := syscall.BytePtrFromString("/proc/self/ns/net")
	if err != nil {
		return err
	}
	pBytes, err := syscall.BytePtrFromString(p)
	if err != nil {
		return err
	}
	beforeFork()

	pid, _, errno := syscall.RawSyscall6(syscall.SYS_CLONE, uintptr(syscall.SIGCHLD)|unix.CLONE_NEWNET, 0, 0, 0, 0, 0)
	if errno != 0 {
		afterFork()
		return errno
	}

	if pid != 0 {
		afterFork()
		var ws unix.WaitStatus
		_, err = unix.Wait4(int(pid), &ws, 0, nil)
		for err == syscall.EINTR {
			_, err = unix.Wait4(int(pid), &ws, 0, nil)
		}

		if err != nil {
			return errors.Wrapf(err, "failed to find pid=%d process", pid)
		}
		errno = syscall.Errno(ws.ExitStatus())
		if errno != 0 {
			return errors.Wrap(errno, "failed to mount")
		}
		return nil
	}
	afterForkInChild()
	_, _, errno = syscall.RawSyscall6(syscall.SYS_MOUNT, uintptr(unsafe.Pointer(procNetNSBytes)), uintptr(unsafe.Pointer(pBytes)), 0, uintptr(unix.MS_BIND), 0, 0)
	syscall.RawSyscall(syscall.SYS_EXIT, uintptr(errno), 0, 0)
	panic("unreachable")
}
