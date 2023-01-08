// Copyright 2014 Mikio Hara. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package tcp

import (
	"encoding/binary"
	"unsafe"
)

var options = [soMax]option{
	soBuffered: {0, sysFIONREAD},
}

func (nl *pfiocNatlook) rdPort() int {
	return int(binary.BigEndian.Uint16((*[2]byte)(unsafe.Pointer(&nl.Rdport))[:]))
}

func (nl *pfiocNatlook) setPort(remote, local int) {
	binary.BigEndian.PutUint16((*[2]byte)(unsafe.Pointer(&nl.Sport))[:], uint16(remote))
	binary.BigEndian.PutUint16((*[2]byte)(unsafe.Pointer(&nl.Dport))[:], uint16(local))
}
