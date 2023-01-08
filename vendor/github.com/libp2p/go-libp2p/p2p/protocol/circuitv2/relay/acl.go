package relay

import (
	"github.com/libp2p/go-libp2p/core/peer"

	ma "github.com/multiformats/go-multiaddr"
)

// ACLFilter is an Access Control mechanism for relayed connect.
type ACLFilter interface {
	// AllowReserve returns true if a reservation from a peer with the given peer ID and multiaddr
	// is allowed.
	AllowReserve(p peer.ID, a ma.Multiaddr) bool
	// AllowConnect returns true if a source peer, with a given multiaddr is allowed to connect
	// to a destination peer.
	AllowConnect(src peer.ID, srcAddr ma.Multiaddr, dest peer.ID) bool
}
