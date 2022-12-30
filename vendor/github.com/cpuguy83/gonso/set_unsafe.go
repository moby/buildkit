package gonso

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// doClone creates a new process in with whatever clone flags are supplied and any namespaces found in the Set.
// CLONE_FILES and CLONE_CLEAR_SIGHAND are always set.
//
// The child process may exit with a non-zero exit codde if setting up the namespaces from the passed in Set fails.
// The child process will exit with a zero exit code if the pipe is closed or written to.
//
// It is the callers responsibility to close the write end of the pipe and to wait for the child process to exit.
//
// This function is useful for operating on or collecting the namespace fd's of the child process.
func doClone(s Set, flags, pipeFd int) (pid int, _ error) {
	buf := make([]byte, 1)
	_p0 := unsafe.Pointer(&buf[0])

	if err := s.set(true); err != nil {
		return 0, err
	}

	var usernsFd int
	// If `flags` contains CLONE_NEWUSER then the call to clone will create a
	// new usernamespace. We don't want to try and set any user namespace from
	// the `Set` in this case because we want the new userns not the old one.
	// It will also cause EPERM if we try to set the user namespace from the
	// `Set` becasue we'll already be in the new user namespace which doesn't
	// have perms in the old user namespace.
	if flags&unix.CLONE_NEWUSER == 0 {
		usernsFd = s.fds[unix.CLONE_NEWUSER]
	}

	// Handle overflow of untyped int on 32-bit platforms
	clearSigHand := int64(unix.CLONE_CLEAR_SIGHAND)

	beforeFork()
	pidptr, _, errno := unix.RawSyscall6(unix.SYS_CLONE, uintptr(unix.SIGCHLD)|uintptr(clearSigHand)|unix.CLONE_FILES|uintptr(flags), 0, 0, 0, 0, 0)
	if errno != 0 {
		afterFork()
		return 0, fmt.Errorf("error calling clone: %w", errno)
	}
	pid = int(pidptr)

	if pid != 0 {
		afterFork()
		return pid, nil
	}

	// child process
	if usernsFd > 0 {
		_, _, errno := unix.RawSyscall(unix.SYS_SETNS, uintptr(usernsFd), uintptr(unix.CLONE_NEWUSER), 0)
		if errno != 0 {
			unix.RawSyscall(unix.SYS_EXIT_GROUP, uintptr(errno), 0, 0)
			panic("unreachable")
		}
	}

	// block until the parent process closes this fd
	unix.RawSyscall(unix.SYS_READ, uintptr(pipeFd), uintptr(_p0), uintptr(len(buf)))
	unix.RawSyscall(unix.SYS_EXIT_GROUP, uintptr(0), 0, 0)
	panic("unreachable")
}

func cloneNs(s Set, flags int, uidMaps, gidMaps []IDMap) (Set, error) {
	type result struct {
		s   Set
		err error
	}

	ch := make(chan result, 1)
	go func() {
		runtime.LockOSThread()

		set, err := func() (retSet Set, retErr error) {
			var pipe [2]int
			if err := make_pipe(pipe[:]); err != nil {
				return Set{}, fmt.Errorf("error creating pipe: %w", err)
			}

			defer func() {
				if retErr != nil {
					retSet.Close()
				}
			}()

			pid, err := doClone(s, flags, pipe[0])
			if err != nil {
				sys_close(pipe[0])
				sys_close(pipe[1])
				return Set{}, err
			}

			chExit := make(chan error, 1)
			go func() {
				status, err := wait(int(pid))
				if err != nil {
					chExit <- fmt.Errorf("error waiting for child: %w", err)
					return
				}
				code := status.ExitStatus()
				if code != 0 {
					chExit <- fmt.Errorf("child exited with code %d: %w", code, unix.Errno(code))
					return
				}
				chExit <- nil
			}()

			defer func() {
				sys_close(pipe[0])
				sys_close(pipe[1])

				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				select {
				case <-ctx.Done():
					kill(int(pid))
					err2 := <-chExit
					if retErr == nil {
						retErr = err2
					} else {
						retErr = fmt.Errorf("%w: %v", retErr, err2)
					}
				case err2 := <-chExit:
					if retErr == nil {
						retErr = err2
					} else {
						retErr = fmt.Errorf("%w: %v", retErr, err2)
					}
				}
			}()

			if err := setIDMaps(int(pid), uidMaps, gidMaps); err != nil {
				return Set{}, fmt.Errorf("error setting id maps: %w", err)
			}

			return FromDir(fmt.Sprintf("/proc/%d/ns", pid), flags)
		}()

		ch <- result{s: set, err: err}
	}()

	r := <-ch
	return r.s, r.err
}

func setIDMaps(pid int, uidMaps, gidMaps []IDMap) error {
	buf := bytes.NewBuffer(nil)
	if len(uidMaps) > 0 {
		f, err := os.OpenFile(fmt.Sprintf("/proc/%d/uid_map", pid), os.O_WRONLY, 0)
		if err != nil {
			return err
		}
		defer f.Close()
		for _, m := range uidMaps {
			_, err := buf.Write([]byte(idMap(m) + "\n"))
			if err != nil {
				return err
			}
		}

		if _, err := io.Copy(f, buf); err != nil {
			return err
		}
	}

	if len(gidMaps) > 0 {
		buf.Reset()
		f, err := os.OpenFile(fmt.Sprintf("/proc/%d/gid_map", pid), os.O_WRONLY, 0)
		if err != nil {
			return err
		}
		defer f.Close()

		for _, m := range uidMaps {
			_, err := buf.Write([]byte(idMap(m) + "\n"))
			if err != nil {
				return err
			}
		}

		if _, err := io.Copy(f, buf); err != nil {
			return err
		}
	}

	return nil
}

func idMap(mapping syscall.SysProcIDMap) string {
	return fmt.Sprintf("%d %d %d", mapping.ContainerID, mapping.HostID, mapping.Size)
}
