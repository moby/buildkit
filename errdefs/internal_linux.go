//go:build linux
// +build linux

package errdefs

import (
	"syscall"

	"golang.org/x/sys/unix"
)

func syscallErrors() map[syscall.Errno]struct{} {
	return map[syscall.Errno]struct{}{
		unix.EIO:             {}, // I/O error
		unix.ENOMEM:          {}, // Out of memory
		unix.EFAULT:          {}, // Bad address
		unix.ENOSPC:          {}, // No space left on device
		unix.ENOTRECOVERABLE: {}, // State not recoverable
		unix.EHWPOISON:       {}, // Memory page has hardware error
	}
}
