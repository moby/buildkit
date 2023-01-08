// Copyright 2017 Mikio Hara. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package tcp

import (
	"syscall"
	"unsafe"
)

const (
	sysSIOCINQ  = 0x541b
	sysSIOCOUTQ = 0x5411
)

func ioctl(s uintptr, ioc int, b []byte) error {
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, s, uintptr(ioc), uintptr(unsafe.Pointer(&b[0]))); errno != 0 {
		return error(errno)
	}
	return nil
}

const (
	sysSETSOCKOPT = 0xe
	sysGETSOCKOPT = 0xf
)

func socketcall(call, a0, a1, a2, a3, a4, a5 uintptr) (uintptr, syscall.Errno)

func setsockopt(s uintptr, level, name int, b []byte) error {
	if _, errno := socketcall(sysSETSOCKOPT, s, uintptr(level), uintptr(name), uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)), 0); errno != 0 {
		return error(errno)
	}
	return nil
}

func getsockopt(s uintptr, level, name int, b []byte) (int, error) {
	l := uint32(len(b))
	if _, errno := socketcall(sysGETSOCKOPT, s, uintptr(level), uintptr(name), uintptr(unsafe.Pointer(&b[0])), uintptr(unsafe.Pointer(&l)), 0); errno != 0 {
		return int(l), error(errno)
	}
	return int(l), nil
}
