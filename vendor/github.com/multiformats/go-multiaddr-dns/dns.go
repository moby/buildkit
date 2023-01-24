package madns

import (
	ma "github.com/multiformats/go-multiaddr"
)

// Extracted from source of truth for multicodec codes: https://github.com/multiformats/multicodec
const (
	// Deprecated: use ma.P_DNS
	P_DNS = ma.P_DNS
	// Deprecated: use ma.P_DNS4
	P_DNS4 = ma.P_DNS4
	// Deprecated: use ma.P_DNS6
	P_DNS6 = ma.P_DNS6
	// Deprecated: use ma.P_DNSADDR
	P_DNSADDR = ma.P_DNSADDR
)

// Deprecated: use ma.ProtocolWithCode(P_DNS)
var DnsProtocol = ma.ProtocolWithCode(P_DNS)

// Deprecated: use ma.ProtocolWithCode(P_DNS4)
var Dns4Protocol = ma.ProtocolWithCode(P_DNS4)

// Deprecated: use ma.ProtocolWithCode(P_DNS6)
var Dns6Protocol = ma.ProtocolWithCode(P_DNS6)

// Deprecated: use ma.ProtocolWithCode(P_DNSADDR)
var DnsaddrProtocol = ma.ProtocolWithCode(P_DNSADDR)
