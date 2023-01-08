// Copyright 2016 Mikio Hara. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// +build solaris

package tcpopt

import "time"

const (
	sysSOL_SOCKET = 0xffff

	sysSO_SNDBUF    = 0x1001
	sysSO_RCVBUF    = 0x1002
	sysSO_KEEPALIVE = 0x8

	sysTCP_NODELAY                   = 0x1
	sysTCP_MAXSEG                    = 0x2
	sysTCP_KEEPALIVE                 = 0x8
	sysTCP_KEEPALIVE_THRESHOLD       = 0x16
	sysTCP_KEEPALIVE_ABORT_THRESHOLD = 0x17
	sysTCP_KEEPIDLE                  = 0x22
	sysTCP_KEEPCNT                   = 0x23
	sysTCP_KEEPINTVL                 = 0x24
	sysTCP_CORK                      = 0x18
	sysSO_ERROR                      = 0x1007
)

var options = [soMax]option{
	soNodelay:   {ianaProtocolTCP, sysTCP_NODELAY, 0},
	soMaxseg:    {ianaProtocolTCP, sysTCP_MAXSEG, 0},
	soSndbuf:    {sysSOL_SOCKET, sysSO_SNDBUF, 0},
	soRcvbuf:    {sysSOL_SOCKET, sysSO_RCVBUF, 0},
	soKeepalive: {sysSOL_SOCKET, sysSO_KEEPALIVE, 0},
	soKeepidle:  {ianaProtocolTCP, sysTCP_KEEPIDLE, time.Second},
	soKeepintvl: {ianaProtocolTCP, sysTCP_KEEPINTVL, time.Second},
	soKeepcnt:   {ianaProtocolTCP, sysTCP_KEEPCNT, 0},
	soCork:      {ianaProtocolTCP, sysTCP_CORK, 0},
	soError:     {sysSOL_SOCKET, sysSO_ERROR, 0},
}

var parsers = map[int64]func([]byte) (Option, error){
	ianaProtocolTCP<<32 | sysTCP_NODELAY:   parseNoDelay,
	ianaProtocolTCP<<32 | sysTCP_MAXSEG:    parseMSS,
	sysSOL_SOCKET<<32 | sysSO_SNDBUF:       parseSendBuffer,
	sysSOL_SOCKET<<32 | sysSO_RCVBUF:       parseReceiveBuffer,
	sysSOL_SOCKET<<32 | sysSO_KEEPALIVE:    parseKeepAlive,
	ianaProtocolTCP<<32 | sysTCP_KEEPIDLE:  parseKeepAliveIdleInterval,
	ianaProtocolTCP<<32 | sysTCP_KEEPINTVL: parseKeepAliveProbeInterval,
	ianaProtocolTCP<<32 | sysTCP_KEEPCNT:   parseKeepAliveProbeCount,
	ianaProtocolTCP<<32 | sysTCP_CORK:      parseCork,
	sysSOL_SOCKET<<32 | sysSO_ERROR:        parseError,
}
