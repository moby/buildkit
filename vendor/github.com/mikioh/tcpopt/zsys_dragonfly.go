// Created by cgo -godefs - DO NOT EDIT
// cgo -godefs defs_dragonfly.go

package tcpopt

const (
	sysSOL_SOCKET = 0xffff

	sysSO_KEEPALIVE = 0x8
	sysSO_SNDBUF    = 0x1001
	sysSO_RCVBUF    = 0x1002
	sysSO_ERROR     = 0x1007

	sysTCP_NODELAY   = 0x1
	sysTCP_MAXSEG    = 0x2
	sysTCP_KEEPIDLE  = 0x100
	sysTCP_KEEPINTVL = 0x200
	sysTCP_KEEPCNT   = 0x400
	sysTCP_NOPUSH    = 0x4
)
