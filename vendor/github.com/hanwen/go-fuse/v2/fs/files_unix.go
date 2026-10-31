//go:build !darwin

package fs

import (
	"syscall"
	"time"
	"unsafe"

	"github.com/hanwen/go-fuse/v2/fuse"
)

// Utimens - file handle based version of loopbackFileSystem.Utimens()
func (f *LoopbackFile) utimens(a *time.Time, m *time.Time) syscall.Errno {
	var ts [2]syscall.Timespec
	ts[0] = fuse.UtimeToTimespec(a)
	ts[1] = fuse.UtimeToTimespec(m)
	return f.withFd(func(fd int) syscall.Errno {
		return ToErrno(futimens(fd, &ts))
	})
}

// futimens - futimens(3) calls utimensat(2) with "pathname" set to null and
// "flags" set to zero
func futimens(fd int, times *[2]syscall.Timespec) (err error) {
	_, _, e1 := syscall.Syscall6(syscall.SYS_UTIMENSAT, uintptr(fd), 0, uintptr(unsafe.Pointer(times)), uintptr(0), 0, 0)
	if e1 != 0 {
		err = syscall.Errno(e1)
	}
	return
}
