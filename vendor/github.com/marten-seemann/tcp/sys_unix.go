// Copyright 2014 Mikio Hara. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// +build darwin dragonfly freebsd linux,!s390x,!386 netbsd openbsd

package tcp

import (
	"syscall"
	"unsafe"
)

func ioctl(s uintptr, ioc int, b []byte) error {
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, s, uintptr(ioc), uintptr(unsafe.Pointer(&b[0]))); errno != 0 {
		return error(errno)
	}
	return nil
}

func setsockopt(s uintptr, level, name int, b []byte) error {
	if _, _, errno := syscall.Syscall6(syscall.SYS_SETSOCKOPT, s, uintptr(level), uintptr(name), uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)), 0); errno != 0 {
		return error(errno)
	}
	return nil
}

func getsockopt(s uintptr, level, name int, b []byte) (int, error) {
	l := uint32(len(b))
	if _, _, errno := syscall.Syscall6(syscall.SYS_GETSOCKOPT, s, uintptr(level), uintptr(name), uintptr(unsafe.Pointer(&b[0])), uintptr(unsafe.Pointer(&l)), 0); errno != 0 {
		return int(l), error(errno)
	}
	return int(l), nil
}
