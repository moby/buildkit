// Copyright 2016 Mikio Hara. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// +build !darwin,!freebsd,!linux,!netbsd

package tcpinfo

import (
	"errors"

	"github.com/mikioh/tcpopt"
)

var options [soMax]option

// Marshal implements the Marshal method of tcpopt.Option interface.
func (i *Info) Marshal() ([]byte, error) {
	return nil, errors.New("operation not supported")
}

// A SysInfo represents platform-specific information.
type SysInfo struct{}

func parseInfo(b []byte) (tcpopt.Option, error) {
	return nil, errors.New("operation not supported")
}

func parseCCAlgorithmInfo(name string, b []byte) (CCAlgorithmInfo, error) {
	return nil, errors.New("operation not supported")
}
