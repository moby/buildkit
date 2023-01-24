// Copyright 2014 Mikio Hara. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package tcp

import (
	"encoding/binary"
	"unsafe"
)

var options = [soMax]option{
	soBuffered:  {0, sysFIONREAD},
	soAvailable: {sysSOL_SOCKET, sysSO_NWRITE},
}

func (nl *pfiocNatlook) rdPort() int {
	return int(binary.BigEndian.Uint16(nl.Rdxport[:2]))
}

func (nl *pfiocNatlook) setPort(remote, local int) {
	binary.BigEndian.PutUint16((*[2]byte)(unsafe.Pointer(&nl.Sxport))[:2], uint16(remote))
	binary.BigEndian.PutUint16((*[2]byte)(unsafe.Pointer(&nl.Dxport))[:2], uint16(local))
}
