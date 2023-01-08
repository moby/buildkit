// Created by cgo -godefs - DO NOT EDIT
// cgo -godefs defs_netbsd.go

package tcpopt

const (
	sysSOL_SOCKET = 0xffff

	sysSO_KEEPALIVE = 0x8
	sysSO_SNDBUF    = 0x1001
	sysSO_RCVBUF    = 0x1002
	sysSO_ERROR     = 0x1007

	sysTCP_NODELAY   = 0x1
	sysTCP_MAXSEG    = 0x2
	sysTCP_KEEPIDLE  = 0x3
	sysTCP_KEEPINTVL = 0x5
	sysTCP_KEEPCNT   = 0x6
)
