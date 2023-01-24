// Created by cgo -godefs - DO NOT EDIT
// cgo -godefs defs_linux.go

// +build !mips64
// +build !mips64le
// +build linux

package tcpopt

const (
	sysSOL_SOCKET = 0x1

	sysSO_KEEPALIVE = 0x9
	sysSO_SNDBUF    = 0x7
	sysSO_RCVBUF    = 0x8
	sysSO_ERROR     = 0x4

	sysTCP_NODELAY       = 0x1
	sysTCP_MAXSEG        = 0x2
	sysTCP_KEEPIDLE      = 0x4
	sysTCP_KEEPINTVL     = 0x5
	sysTCP_KEEPCNT       = 0x6
	sysTCP_CORK          = 0x3
	sysTCP_NOTSENT_LOWAT = 0x19
)
