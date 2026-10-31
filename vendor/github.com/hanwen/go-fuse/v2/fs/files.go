// Copyright 2019 the Go-FUSE Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package fs

import (
	"context"
	"errors"
	"os"
	"syscall"
	"unsafe"

	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/hanwen/go-fuse/v2/internal/fallocate"
	"github.com/hanwen/go-fuse/v2/internal/ioctl"
	"golang.org/x/sys/unix"
)

// NewLoopbackFile creates a FileHandle out of a file descriptor.
//
// This function is hard to use correctly. Most callers should use
// NewLoopbackFileFromOS instead.
//
// All operations are implemented. NewLoopbackFile takes ownership of the
// file descriptor: it is closed on Release, or when the FileHandle is
// garbage collected.
func NewLoopbackFile(fd int) FileHandle {
	// Wart: this should return *LoopbackFile instead.
	return NewLoopbackFileFromOS(os.NewFile(uintptr(fd), ""))
}

// NewLoopbackFileFromOS creates a FileHandle out of a *os.File. It
// takes ownership of the file: it is closed on Release, and callers
// should not use it afterwards.
func NewLoopbackFileFromOS(f *os.File) *LoopbackFile {
	return &LoopbackFile{f: f}
}

// LoopbackFile is a FileHandle that implements all the FileXxxx
// interfaces by forwarding all calls to the underlying file
// descriptor. Create an instance by casting the return value of
// NewLoopbackFile(). This type is public so it can be used as a basis
// for other FileHandle implementations.
type LoopbackFile struct {
	f *os.File
}

var _ = (FileHandle)((*LoopbackFile)(nil))
var _ = (FileReleaser)((*LoopbackFile)(nil))
var _ = (FileGetattrer)((*LoopbackFile)(nil))
var _ = (FileReader)((*LoopbackFile)(nil))
var _ = (FileWriter)((*LoopbackFile)(nil))
var _ = (FileGetlker)((*LoopbackFile)(nil))
var _ = (FileSetlker)((*LoopbackFile)(nil))
var _ = (FileSetlkwer)((*LoopbackFile)(nil))
var _ = (FileLseeker)((*LoopbackFile)(nil))
var _ = (FileFlusher)((*LoopbackFile)(nil))
var _ = (FileFsyncer)((*LoopbackFile)(nil))
var _ = (FileSetattrer)((*LoopbackFile)(nil))
var _ = (FileAllocater)((*LoopbackFile)(nil))
var _ = (FilePassthroughFder)((*LoopbackFile)(nil))
var _ = (FileIoctler)((*LoopbackFile)(nil))

func (f *LoopbackFile) withFd(fn func(fd int) syscall.Errno) syscall.Errno {
	sc, err := f.f.SyscallConn()
	if err != nil {
		return syscall.EBADF
	}
	errno := syscall.EBADF
	sc.Control(func(fd uintptr) {
		errno = fn(int(fd))
	})
	return errno
}

func (f *LoopbackFile) PassthroughFd() (int, bool) {
	// The fd outlives the Control call below. It must stay alive
	// until the the fd is reported to the kernel. (In the normal
	// case, it survives until Release)
	var fd int
	errno := f.withFd(func(d int) syscall.Errno {
		fd = d
		return OK
	})
	return fd, errno == OK
}

func (f *LoopbackFile) Read(ctx context.Context, buf []byte, off int64) (res fuse.ReadResult, errno syscall.Errno) {
	errno = f.withFd(func(fd int) syscall.Errno {
		res = fuse.ReadResultFd(uintptr(fd), off, len(buf))
		return OK
	})
	return res, errno
}

func (f *LoopbackFile) Write(ctx context.Context, data []byte, off int64) (uint32, syscall.Errno) {
	var n int
	errno := f.withFd(func(fd int) syscall.Errno {
		var err error
		n, err = syscall.Pwrite(fd, data, off)
		return ToErrno(err)
	})
	return uint32(n), errno
}

func (f *LoopbackFile) Release(ctx context.Context) syscall.Errno {
	err := f.f.Close()
	if errors.Is(err, os.ErrClosed) {
		return syscall.EBADF
	}
	return ToErrno(err)
}

func (f *LoopbackFile) Flush(ctx context.Context) syscall.Errno {
	return f.withFd(func(fd int) syscall.Errno {
		// Since Flush() may be called for each dup'd fd, we don't
		// want to really close the file, we just want to flush. This
		// is achieved by closing a dup'd fd.
		newFd, err := syscall.Dup(fd)
		if err != nil {
			return ToErrno(err)
		}
		return ToErrno(syscall.Close(newFd))
	})
}

func (f *LoopbackFile) Fsync(ctx context.Context, flags uint32) (errno syscall.Errno) {
	return f.withFd(func(fd int) syscall.Errno {
		return ToErrno(syscall.Fsync(fd))
	})
}

const (
	_OFD_GETLK  = 36
	_OFD_SETLK  = 37
	_OFD_SETLKW = 38
)

func (f *LoopbackFile) Getlk(ctx context.Context, owner uint64, lk *fuse.FileLock, flags uint32, out *fuse.FileLock) (errno syscall.Errno) {
	return f.withFd(func(fd int) syscall.Errno {
		flk := syscall.Flock_t{}
		lk.ToFlockT(&flk)
		errno := ToErrno(syscall.FcntlFlock(uintptr(fd), _OFD_GETLK, &flk))
		out.FromFlockT(&flk)
		return errno
	})
}

func (f *LoopbackFile) Setlk(ctx context.Context, owner uint64, lk *fuse.FileLock, flags uint32) (errno syscall.Errno) {
	return f.setLock(ctx, owner, lk, flags, false)
}

func (f *LoopbackFile) Setlkw(ctx context.Context, owner uint64, lk *fuse.FileLock, flags uint32) (errno syscall.Errno) {
	return f.setLock(ctx, owner, lk, flags, true)
}

func (f *LoopbackFile) setLock(ctx context.Context, owner uint64, lk *fuse.FileLock, flags uint32, blocking bool) (errno syscall.Errno) {
	return f.withFd(func(fd int) syscall.Errno {
		if (flags & fuse.FUSE_LK_FLOCK) != 0 {
			var op int
			switch lk.Typ {
			case syscall.F_RDLCK:
				op = syscall.LOCK_SH
			case syscall.F_WRLCK:
				op = syscall.LOCK_EX
			case syscall.F_UNLCK:
				op = syscall.LOCK_UN
			default:
				return syscall.EINVAL
			}
			if !blocking {
				op |= syscall.LOCK_NB
			}
			return ToErrno(syscall.Flock(fd, op))
		} else {
			flk := syscall.Flock_t{}
			lk.ToFlockT(&flk)
			var op int
			if blocking {
				op = _OFD_SETLKW
			} else {
				op = _OFD_SETLK
			}
			return ToErrno(syscall.FcntlFlock(uintptr(fd), op, &flk))
		}
	})
}

func (f *LoopbackFile) Setattr(ctx context.Context, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	if errno := f.setAttr(ctx, in); errno != 0 {
		return errno
	}

	return f.Getattr(ctx, out)
}

func (f *LoopbackFile) fchmod(mode uint32) syscall.Errno {
	return f.withFd(func(fd int) syscall.Errno {
		return ToErrno(syscall.Fchmod(fd, mode))
	})
}

func (f *LoopbackFile) fchown(uid, gid int) syscall.Errno {
	return f.withFd(func(fd int) syscall.Errno {
		return ToErrno(syscall.Fchown(fd, uid, gid))
	})
}

func (f *LoopbackFile) ftruncate(sz uint64) syscall.Errno {
	return f.withFd(func(fd int) syscall.Errno {
		return ToErrno(syscall.Ftruncate(fd, int64(sz)))
	})
}

func (f *LoopbackFile) setAttr(ctx context.Context, in *fuse.SetAttrIn) syscall.Errno {
	var errno syscall.Errno
	if mode, ok := in.GetMode(); ok {
		if errno := f.fchmod(mode); errno != 0 {
			return errno
		}
	}

	uid32, uOk := in.GetUID()
	gid32, gOk := in.GetGID()
	if uOk || gOk {
		uid := -1
		gid := -1

		if uOk {
			uid = int(uid32)
		}
		if gOk {
			gid = int(gid32)
		}
		if errno := f.fchown(uid, gid); errno != 0 {
			return errno
		}
	}

	// Truncate before setting times, so an explicit mtime is not
	// clobbered by the truncate.
	if sz, ok := in.GetSize(); ok {
		if errno := f.ftruncate(sz); errno != 0 {
			return errno
		}
	}

	mtime, mok := in.GetMTime()
	atime, aok := in.GetATime()

	if mok || aok {
		ap := &atime
		mp := &mtime
		if !aok {
			ap = nil
		}
		if !mok {
			mp = nil
		}
		errno = f.utimens(ap, mp)
		if errno != 0 {
			return errno
		}
	}
	return OK
}

func (f *LoopbackFile) Getattr(ctx context.Context, a *fuse.AttrOut) syscall.Errno {
	return f.withFd(func(fd int) syscall.Errno {
		st := syscall.Stat_t{}
		err := syscall.Fstat(fd, &st)
		if err != nil {
			return ToErrno(err)
		}
		a.FromStat(&st)

		return OK
	})
}

func (f *LoopbackFile) Lseek(ctx context.Context, off uint64, whence uint32) (uint64, syscall.Errno) {
	var n int64
	errno := f.withFd(func(fd int) syscall.Errno {
		var err error
		n, err = unix.Seek(fd, int64(off), int(whence))
		return ToErrno(err)
	})
	return uint64(n), errno
}

func (f *LoopbackFile) Allocate(ctx context.Context, off uint64, sz uint64, mode uint32) syscall.Errno {
	return f.withFd(func(fd int) syscall.Errno {
		return ToErrno(fallocate.Fallocate(fd, mode, int64(off), int64(sz)))
	})
}

func (f *LoopbackFile) Ioctl(ctx context.Context, cmd uint32, arg uint64, input []byte, output []byte) (result int32, errno syscall.Errno) {
	errno = f.withFd(func(fd int) syscall.Errno {
		argWord := uintptr(arg)
		ioc := ioctl.Command(cmd)
		if ioc.Read() && ioc.Write() {
			// The kernel updates the buffer in place.
			copy(output, input)
			argWord = uintptr(unsafe.Pointer(&output[0]))
		} else if ioc.Write() {
			argWord = uintptr(unsafe.Pointer(&input[0]))
		} else if ioc.Read() {
			argWord = uintptr(unsafe.Pointer(&output[0]))
		}

		res, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), uintptr(cmd), argWord)
		result = int32(res)
		return errno
	})
	return result, errno
}
