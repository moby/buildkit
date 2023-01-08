// Created by cgo -godefs - DO NOT EDIT
// cgo -godefs defs_darwin.go

package tcp

const (
	sysSOL_SOCKET = 0xffff

	sysFIONREAD = 0x4004667f

	sysSO_NREAD     = 0x1020
	sysSO_NWRITE    = 0x1024
	sysSO_NUMRCVPKT = 0x1112

	sysAF_INET  = 0x2
	sysAF_INET6 = 0x1e

	sysPF_INOUT = 0
	sysPF_IN    = 1
	sysPF_OUT   = 2

	sysDIOCNATLOOK = 0xc0544417
)

type sockaddrStorage struct {
	Len         uint8
	Family      uint8
	X__ss_pad1  [6]int8
	X__ss_align int64
	X__ss_pad2  [112]int8
}

type sockaddr struct {
	Len    uint8
	Family uint8
	Data   [14]int8
}

type sockaddrInet struct {
	Len    uint8
	Family uint8
	Port   uint16
	Addr   [4]byte /* in_addr */
	Zero   [8]int8
}

type sockaddrInet6 struct {
	Len      uint8
	Family   uint8
	Port     uint16
	Flowinfo uint32
	Addr     [16]byte /* in6_addr */
	Scope_id uint32
}

type pfiocNatlook struct {
	Saddr     [16]byte /* pf_addr */
	Daddr     [16]byte /* pf_addr */
	Rsaddr    [16]byte /* pf_addr */
	Rdaddr    [16]byte /* pf_addr */
	Sxport    [4]byte
	Dxport    [4]byte
	Rsxport   [4]byte
	Rdxport   [4]byte
	Af        uint8
	Proto     uint8
	Variant   uint8
	Direction uint8
}

const (
	sizeofSockaddrStorage = 0x80
	sizeofSockaddr        = 0x10
	sizeofSockaddrInet    = 0x10
	sizeofSockaddrInet6   = 0x1c
	sizeofPfiocNatlook    = 0x54
)
