// Copyright 2016 Mikio Hara. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package tcpopt

import (
	"encoding/binary"
	"time"
	"unsafe"
)

var nativeEndian binary.ByteOrder

func init() {
	i := uint32(1)
	b := (*[4]byte)(unsafe.Pointer(&i))
	if b[0] == 1 {
		nativeEndian = binary.LittleEndian
	} else {
		nativeEndian = binary.BigEndian
	}
}

func boolint32(b bool) int32 {
	if b {
		return 1
	}
	return 0
}

func uint32bool(n uint32) bool {
	if n != 0 {
		return true
	}
	return false
}

const (
	ianaProtocolIP   = 0x0
	ianaProtocolTCP  = 0x6
	ianaProtocolIPv6 = 0x29
)

const (
	soNodelay = iota
	soSndbuf
	soRcvbuf
	soKeepalive
	soKeepidle
	soKeepintvl
	soKeepcnt
	soCork
	soNotsentLOWAT
	soError
	soECN
	soMaxseg
	soMax
)

// An option represents a binding for socket option.
type option struct {
	level int           // option level
	name  int           // option name, must be equal or greater than 1
	uot   time.Duration // unit of time
}
