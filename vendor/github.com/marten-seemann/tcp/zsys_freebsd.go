// Created by cgo -godefs - DO NOT EDIT
// cgo -godefs defs_freebsd.go

package tcp

const (
	sysFIONREAD  = 0x4004667f
	sysFIONWRITE = 0x40046677
	sysFIONSPACE = 0x40046676

	sysAF_INET  = 0x2
	sysAF_INET6 = 0x1c

	sysPF_INOUT = 0x0
	sysPF_IN    = 0x1
	sysPF_OUT   = 0x2
	sysPF_FWD   = 0x3

	sysDIOCNATLOOK = 0xc04c4417
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
	Sport     uint16
	Dport     uint16
	Rsport    uint16
	Rdport    uint16
	Af        uint8
	Proto     uint8
	Direction uint8
	Pad_cgo_0 [1]byte
}

const (
	sizeofSockaddrStorage = 0x80
	sizeofSockaddr        = 0x10
	sizeofSockaddrInet    = 0x10
	sizeofSockaddrInet6   = 0x1c
	sizeofPfiocNatlook    = 0x4c
)
