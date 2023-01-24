// Copyright 2014 Mikio Hara. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package tcp

import (
	"errors"
	"os"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/mikioh/tcpopt"
)

var options [soMax]option

func ioctl(s uintptr, ioc int, b []byte) error {
	return errors.New("not implemented")
}

var keepAlive = struct {
	sync.RWMutex
	syscall.TCPKeepalive
}{
	TCPKeepalive: syscall.TCPKeepalive{
		OnOff:    1,
		Time:     uint32(2 * time.Hour / time.Millisecond),
		Interval: uint32(time.Second / time.Millisecond),
	},
}

func setsockopt(s uintptr, level, name int, b []byte) error {
	var kai tcpopt.KeepAliveIdleInterval
	var kap tcpopt.KeepAliveProbeInterval
	if level == kai.Level() && name == kai.Name() {
		keepAlive.Lock()
		defer keepAlive.Unlock()
		prev := keepAlive.Time
		keepAlive.Time = nativeEndian.Uint32(b)
		rv := uint32(0)
		siz := uint32(unsafe.Sizeof(keepAlive))
		if err := syscall.WSAIoctl(syscall.Handle(s), syscall.SIO_KEEPALIVE_VALS, (*byte)(unsafe.Pointer(&keepAlive)), siz, nil, 0, &rv, nil, 0); err != nil {
			keepAlive.Time = prev
			return os.NewSyscallError("wsaioctl", err)
		}
		return nil
	}
	if level == kap.Level() && name == kap.Name() {
		keepAlive.Lock()
		defer keepAlive.Unlock()
		prev := keepAlive.Interval
		keepAlive.Interval = nativeEndian.Uint32(b)
		rv := uint32(0)
		siz := uint32(unsafe.Sizeof(keepAlive))
		if err := syscall.WSAIoctl(syscall.Handle(s), syscall.SIO_KEEPALIVE_VALS, (*byte)(unsafe.Pointer(&keepAlive)), siz, nil, 0, &rv, nil, 0); err != nil {
			keepAlive.Interval = prev
			return os.NewSyscallError("wsaioctl", err)
		}
		return nil
	}
	if len(b) == 4 {
		v := int(nativeEndian.Uint32(b))
		return syscall.SetsockoptInt(syscall.Handle(s), level, name, v)
	}
	return errors.New("not implemented")
}

func getsockopt(s uintptr, level, name int, b []byte) (int, error) {
	return 0, errors.New("not implemented")
}
