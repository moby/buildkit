// Created by cgo -godefs - DO NOT EDIT
// cgo -godefs defs_linux.go

package tcp

const (
	sysSO_ORIGINAL_DST      = 0x50
	sysIP6T_SO_ORIGINAL_DST = 0x50
)

type sockaddrStorage struct {
	Family        uint16
	X__ss_padding [118]int8
	X__ss_align   uint64
}

type sockaddr struct {
	Family uint16
	Data   [14]int8
}

type sockaddrInet struct {
	Family uint16
	Port   uint16
	Addr   [4]byte /* in_addr */
	X__pad [8]uint8
}

type sockaddrInet6 struct {
	Family   uint16
	Port     uint16
	Flowinfo uint32
	Addr     [16]byte /* in6_addr */
	Scope_id uint32
}

const (
	sizeofSockaddrStorage = 0x80
	sizeofSockaddr        = 0x10
	sizeofSockaddrInet    = 0x10
	sizeofSockaddrInet6   = 0x1c
)
