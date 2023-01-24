package autonat

import (
	"github.com/libp2p/go-libp2p/core/network"

	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
)

var _ network.Notifiee = (*AmbientAutoNAT)(nil)

// Listen is part of the network.Notifiee interface
func (as *AmbientAutoNAT) Listen(net network.Network, a ma.Multiaddr) {}

// ListenClose is part of the network.Notifiee interface
func (as *AmbientAutoNAT) ListenClose(net network.Network, a ma.Multiaddr) {}

// Connected is part of the network.Notifiee interface
func (as *AmbientAutoNAT) Connected(net network.Network, c network.Conn) {
	if c.Stat().Direction == network.DirInbound &&
		manet.IsPublicAddr(c.RemoteMultiaddr()) {
		select {
		case as.inboundConn <- c:
		default:
		}
	}
}

// Disconnected is part of the network.Notifiee interface
func (as *AmbientAutoNAT) Disconnected(net network.Network, c network.Conn) {}
