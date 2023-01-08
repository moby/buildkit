// Copyright 2016 Mikio Hara. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package tcp

import (
	"syscall"
	"unsafe"
)

var options [soMax]option

func rtioctl(s uintptr, ioc uintptr, arg uintptr) syscall.Errno

func ioctl(s uintptr, ioc int, b []byte) error {
	if errno := rtioctl(s, uintptr(ioc), uintptr(unsafe.Pointer(&b[0]))); errno != 0 {
		return error(errno)
	}
	return nil
}

//go:cgo_import_dynamic libcGetsockopt __xnet_getsockopt "libsocket.so"
//go:cgo_import_dynamic libcSetsockopt setsockopt "libsocket.so"

//go:linkname libcGetsockopt libcGetsockopt
//go:linkname libcSetsockopt libcSetsockopt

var (
	libcGetsockopt uintptr
	libcSetsockopt uintptr
)

func sysvicall6(trap, nargs, a1, a2, a3, a4, a5, a6 uintptr) (uintptr, uintptr, syscall.Errno)

func setsockopt(s uintptr, level, name int, b []byte) error {
	if _, _, errno := sysvicall6(uintptr(unsafe.Pointer(&libcSetsockopt)), 5, s, uintptr(level), uintptr(name), uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)), 0); errno != 0 {
		return error(errno)
	}
	return nil
}

func getsockopt(s uintptr, level, name int, b []byte) (int, error) {
	l := uint32(len(b))
	if _, _, errno := sysvicall6(uintptr(unsafe.Pointer(&libcGetsockopt)), 5, s, uintptr(level), uintptr(name), uintptr(unsafe.Pointer(&b[0])), uintptr(unsafe.Pointer(&l)), 0); errno != 0 {
		return int(l), error(errno)
	}
	return int(l), nil
}
