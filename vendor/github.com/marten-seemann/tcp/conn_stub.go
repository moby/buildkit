// Copyright 2016 Mikio Hara. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// +build !darwin,!dragonfly,!freebsd,!linux,!openbsd

package tcp

import (
	"errors"
	"net"
)

func (c *Conn) originalDst(la, ra *net.TCPAddr) (net.Addr, error) {
	return nil, errors.New("not implemented")
}
