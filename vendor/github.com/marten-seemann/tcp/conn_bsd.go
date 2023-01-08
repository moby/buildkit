// Copyright 2016 Mikio Hara. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// +build darwin dragonfly freebsd openbsd

package tcp

import (
	"net"
	"os"
	"syscall"
	"unsafe"
)

func (*Conn) originalDst(la, ra *net.TCPAddr) (net.Addr, error) {
	f, err := os.Open("/dev/pf")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fd := f.Fd()
	b := make([]byte, sizeofPfiocNatlook)
	nl := (*pfiocNatlook)(unsafe.Pointer(&b[0]))
	if ra.IP.To4() != nil {
		copy(nl.Saddr[:net.IPv4len], ra.IP.To4())
		copy(nl.Daddr[:net.IPv4len], la.IP.To4())
		nl.Af = sysAF_INET
	}
	if ra.IP.To16() != nil && ra.IP.To4() == nil {
		copy(nl.Saddr[:], ra.IP)
		copy(nl.Daddr[:], la.IP)
		nl.Af = sysAF_INET6
	}
	nl.setPort(ra.Port, la.Port)
	nl.Proto = ianaProtocolTCP
	ioc := uintptr(sysDIOCNATLOOK)
	for _, dir := range []byte{sysPF_OUT, sysPF_IN} {
		nl.Direction = dir
		err = ioctl(fd, int(ioc), b)
		if err == nil || err != syscall.ENOENT {
			break
		}
	}
	if err != nil {
		return nil, os.NewSyscallError("ioctl", err)
	}
	od := new(net.TCPAddr)
	od.Port = nl.rdPort()
	switch nl.Af {
	case sysAF_INET:
		od.IP = make(net.IP, net.IPv4len)
		copy(od.IP, nl.Rdaddr[:net.IPv4len])
	case sysAF_INET6:
		od.IP = make(net.IP, net.IPv6len)
		copy(od.IP, nl.Rdaddr[:])
	}
	return od, nil
}
