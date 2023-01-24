// Copyright 2017 Mikio Hara. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package tcp

import (
	"errors"
	"net"
	"os"
	"runtime"
	"syscall"

	"github.com/mikioh/tcpopt"
)

// A Conn represents an end point that uses TCP connection.
// It allows to set non-portable, platform-dependent TCP-level socket
// options.
type Conn struct {
	net.Conn
	c syscall.RawConn
}

func (c *Conn) ok() bool { return c != nil && c.Conn != nil && c.c != nil }

func (c *Conn) setOption(level, name int, b []byte) error {
	var operr error
	fn := func(s uintptr) {
		operr = setsockopt(s, level, name, b)
	}
	if err := c.c.Control(fn); err != nil {
		return err
	}
	return os.NewSyscallError("setsockopt", operr)
}

func (c *Conn) option(level, name int, b []byte) (int, error) {
	var operr error
	var n int
	fn := func(s uintptr) {
		n, operr = getsockopt(s, level, name, b)
	}
	if err := c.c.Control(fn); err != nil {
		return 0, err
	}
	return n, os.NewSyscallError("getsockopt", operr)
}

func (c *Conn) buffered() int {
	var operr error
	var n int
	fn := func(s uintptr) {
		var b [4]byte
		operr = ioctl(s, options[soBuffered].name, b[:])
		if operr != nil {
			return
		}
		n = int(nativeEndian.Uint32(b[:]))
	}
	err := c.c.Control(fn)
	if err != nil || operr != nil {
		return -1
	}
	return n
}

func (c *Conn) available() int {
	var operr error
	var n int
	fn := func(s uintptr) {
		var b [4]byte
		if runtime.GOOS == "darwin" {
			_, operr = getsockopt(s, options[soAvailable].level, options[soAvailable].name, b[:])
		} else {
			operr = ioctl(s, options[soAvailable].name, b[:])
		}
		if operr != nil {
			return
		}
		n = int(nativeEndian.Uint32(b[:]))
		if runtime.GOOS == "darwin" || runtime.GOOS == "linux" {
			var o tcpopt.SendBuffer
			_, operr = getsockopt(s, o.Level(), o.Name(), b[:])
			if operr != nil {
				return
			}
			n = int(nativeEndian.Uint32(b[:])) - n
		}
	}
	err := c.c.Control(fn)
	if err != nil || operr != nil {
		return -1
	}
	return n
}

// NewConn returns a new end point.
func NewConn(c net.Conn) (*Conn, error) {
	type tcpConn interface {
		SyscallConn() (syscall.RawConn, error)
		SetLinger(int) error
	}
	var _ tcpConn = &net.TCPConn{}
	cc := &Conn{Conn: c}
	switch c := c.(type) {
	case tcpConn:
		var err error
		cc.c, err = c.SyscallConn()
		if err != nil {
			return nil, err
		}
		return cc, nil
	default:
		return nil, errors.New("unknown connection type")
	}
}
