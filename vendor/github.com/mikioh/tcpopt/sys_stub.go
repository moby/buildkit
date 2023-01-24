// Copyright 2016 Mikio Hara. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// +build !darwin,!dragonfly,!freebsd,!linux,!netbsd,!openbsd,!solaris,!windows

package tcpopt

import "errors"

var options [soMax]option

var parsers = map[int64]func([]byte) (Option, error){}

// Marshal implements the Marshal method of Option interface.
func (nd NoDelay) Marshal() ([]byte, error) {
	return nil, errors.New("operation not supported")
}

// Marshal implements the Marshal method of Option interface.
func (mss MSS) Marshal() ([]byte, error) {
	return nil, errors.New("operation not supported")
}

// Marshal implements the Marshal method of Option interface.
func (sb SendBuffer) Marshal() ([]byte, error) {
	return nil, errors.New("operation not supported")
}

// Marshal implements the Marshal method of Option interface.
func (rb ReceiveBuffer) Marshal() ([]byte, error) {
	return nil, errors.New("operation not supported")
}

// Marshal implements the Marshal method of Option interface.
func (ka KeepAlive) Marshal() ([]byte, error) {
	return nil, errors.New("operation not supported")
}

// Marshal implements the Marshal method of Option interface.
func (ka KeepAliveIdleInterval) Marshal() ([]byte, error) {
	return nil, errors.New("operation not supported")
}

// Marshal implements the Marshal method of Option interface.
func (ka KeepAliveProbeInterval) Marshal() ([]byte, error) {
	return nil, errors.New("operation not supported")
}

// Marshal implements the Marshal method of Option interface.
func (ka KeepAliveProbeCount) Marshal() ([]byte, error) {
	return nil, errors.New("operation not supported")
}

// Marshal implements the Marshal method of Option interface.
func (ck Cork) Marshal() ([]byte, error) {
	return nil, errors.New("operation not supported")
}

// Marshal implements the Marshal method of Option interface.
func (ns NotSentLowWMK) Marshal() ([]byte, error) {
	return nil, errors.New("operation not supported")
}

// Marshal implements the Marshal method of Option interface.
func (e Error) Marshal() ([]byte, error) {
	return nil, errors.New("operation not supported")
}

// Marshal implements the Marshal method of Option interface.
func (cn ECN) Marshal() ([]byte, error) {
	return nil, errors.New("operation not supported")
}
