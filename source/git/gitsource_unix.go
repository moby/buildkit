//go:build !windows
// +build !windows

package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/pkg/errors"
	"golang.org/x/sys/unix"
)

// openSubdirSafe opens a subdirectory safely inside root.
// Linux/Unix version uses openat+O_NOFOLLOW per component to eliminate TOCTOU races.
// Returns a *os.File for the subdirectory.
func openSubdirSafe(root string, subpath string) (*os.File, error) {
	dirfd, err := unix.Open(root, unix.O_DIRECTORY|unix.O_PATH, 0)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to open root %q", root)
	}

	rel := filepath.Clean(subpath)
	rel = strings.TrimPrefix(rel, string(filepath.Separator))
	if rel == "" || rel == "." {
		return os.NewFile(uintptr(dirfd), root), nil
	}

	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		nextfd, err := unix.Openat(
			dirfd,
			part,
			unix.O_NOFOLLOW|unix.O_DIRECTORY|unix.O_PATH,
			0,
		)
		unix.Close(dirfd)
		if err != nil {
			return nil, errors.Wrapf(err, "subpath %q: failed to open component %q", subpath, part)
		}
		dirfd = nextfd
	}

	return os.NewFile(uintptr(dirfd), filepath.Join(root, subpath)), nil
}

// readdirnames returns the names of entries in the directory represented by f.
// On Unix, f may be an O_PATH fd; a readable fd is opened via Openat before reading.
func readdirnames(f *os.File) ([]string, error) {
	readfd, err := unix.Openat(int(f.Fd()), ".", unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return nil, errors.Wrap(err, "failed to open directory for reading")
	}
	d := os.NewFile(uintptr(readfd), f.Name())
	defer d.Close()
	return d.Readdirnames(0)
}

func runWithStandardUmask(ctx context.Context, cmd *exec.Cmd) error {
	errCh := make(chan error)

	go func() {
		defer close(errCh)
		runtime.LockOSThread()

		if err := unshareAndRun(ctx, cmd); err != nil {
			errCh <- err
		}
	}()

	return <-errCh
}

// unshareAndRun needs to be called in a locked thread.
func unshareAndRun(ctx context.Context, cmd *exec.Cmd) error {
	if err := syscall.Unshare(syscall.CLONE_FS); err != nil {
		return err
	}
	syscall.Umask(0022)
	return runProcessGroup(ctx, cmd)
}

func runProcessGroup(ctx context.Context, cmd *exec.Cmd) error {
	cmd.SysProcAttr = &unix.SysProcAttr{
		Setpgid:   true,
		Pdeathsig: unix.SIGTERM,
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	waitDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = unix.Kill(-cmd.Process.Pid, unix.SIGTERM)
			go func() {
				select {
				case <-waitDone:
				case <-time.After(10 * time.Second):
					_ = unix.Kill(-cmd.Process.Pid, unix.SIGKILL)
				}
			}()
		case <-waitDone:
		}
	}()
	err := cmd.Wait()
	close(waitDone)
	return err
}
