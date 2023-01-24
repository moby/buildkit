// Copyright 2016 Mikio Hara. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package tcpopt

var options = [soMax]option{
	soNodelay:   {ianaProtocolTCP, sysTCP_NODELAY, 0},
	soMaxseg:    {ianaProtocolTCP, sysTCP_MAXSEG, 0},
	soSndbuf:    {sysSOL_SOCKET, sysSO_SNDBUF, 0},
	soRcvbuf:    {sysSOL_SOCKET, sysSO_RCVBUF, 0},
	soKeepalive: {sysSOL_SOCKET, sysSO_KEEPALIVE, 0},
	soCork:      {ianaProtocolTCP, sysTCP_NOPUSH, 0},
	soError:     {sysSOL_SOCKET, sysSO_ERROR, 0},
}

var parsers = map[int64]func([]byte) (Option, error){
	ianaProtocolTCP<<32 | sysTCP_NODELAY: parseNoDelay,
	ianaProtocolTCP<<32 | sysTCP_MAXSEG:  parseMSS,
	sysSOL_SOCKET<<32 | sysSO_SNDBUF:     parseSendBuffer,
	sysSOL_SOCKET<<32 | sysSO_RCVBUF:     parseReceiveBuffer,
	sysSOL_SOCKET<<32 | sysSO_KEEPALIVE:  parseKeepAlive,
	ianaProtocolTCP<<32 | sysTCP_NOPUSH:  parseCork,
	sysSOL_SOCKET<<32 | sysSO_ERROR:      parseError,
}
