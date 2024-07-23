//go:build !linux
// +build !linux

package errdefs

import "syscall"

func syscallErrors() map[syscall.Errno]struct{} {
	return nil
}
