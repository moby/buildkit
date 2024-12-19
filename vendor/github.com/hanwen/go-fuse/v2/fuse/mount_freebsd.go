package fuse

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

func callMountFuseFs(mountPoint string, opts *MountOptions) (fd int, err error) {
	bin, err := fusermountBinary()
	if err != nil {
		return 0, err
	}
	f, err := os.OpenFile("/dev/fuse", os.O_RDWR, 0o000)
	if err != nil {
		return -1, err
	}
	cmd := exec.Command(
		bin,
		"--safe",
		"-o", strings.Join(opts.optionsStrings(), ","),
		"3",
		mountPoint,
	)
	cmd.Env = []string{"MOUNT_FUSEFS_CALL_BY_LIB=1"}
	cmd.ExtraFiles = []*os.File{f}

	if err := cmd.Start(); err != nil {
		f.Close()
		return -1, fmt.Errorf("mount_fusefs: %v", err)
	}

	if err := cmd.Wait(); err != nil {
		// see if we have a better error to report
		f.Close()
		return -1, fmt.Errorf("mount_fusefs: %v", err)
	}

	return int(f.Fd()), nil
}

func mount(mountPoint string, opts *MountOptions, ready chan<- error) (fd int, err error) {
	// Using the same logic from libfuse to prevent chaos
	for {
		f, err := os.OpenFile("/dev/null", os.O_RDWR, 0o000)
		if err != nil {
			return -1, err
		}
		if f.Fd() > 2 {
			f.Close()
			break
		}
	}

	// Magic `/dev/fd/N` mountpoint. See the docs for NewServer() for how this
	// works.

	fd = parseFuseFd(mountPoint)
	if fd >= 0 {
		if opts.Debug {
			opts.Logger.Printf("mount: magic mountpoint %q, using fd %d", mountPoint, fd)
		}
	} else {
		// Usual case: mount via the `fusermount` suid helper
		fd, err = callMountFuseFs(mountPoint, opts)
		if err != nil {
			return
		}
	}
	// golang sets CLOEXEC on file descriptors when they are
	// acquired through normal operations (e.g. open).
	// Buf for fd, we have to set CLOEXEC manually
	syscall.CloseOnExec(fd)

	close(ready)
	return fd, err
}

func unmount(mountPoint string, opts *MountOptions) (err error) {
	_ = opts
	return syscall.Unmount(mountPoint, 0)
}

func fusermountBinary() (string, error) {
	binPaths := []string{
		"/sbin/mount_fusefs",
	}

	for _, path := range binPaths {
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}

	return "", fmt.Errorf("no FUSE mount utility found")
}
