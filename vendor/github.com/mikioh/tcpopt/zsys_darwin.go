// Created by cgo -godefs - DO NOT EDIT
// cgo -godefs defs_darwin.go

package tcpopt

const (
	sysSOL_SOCKET = 0xffff

	sysSO_KEEPALIVE = 0x8
	sysSO_SNDBUF    = 0x1001
	sysSO_RCVBUF    = 0x1002
	sysSO_ERROR     = 0x1007

	sysTCP_NODELAY       = 0x1
	sysTCP_MAXSEG        = 0x2
	sysTCP_KEEPALIVE     = 0x10
	sysTCP_KEEPINTVL     = 0x101
	sysTCP_KEEPCNT       = 0x102
	sysTCP_NOPUSH        = 0x4
	sysTCP_ENABLE_ECN    = 0x104
	sysTCP_NOTSENT_LOWAT = 0x201
)
