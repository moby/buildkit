package peer

import (
	"github.com/libp2p/go-libp2p/core/peer"

	ma "github.com/multiformats/go-multiaddr"
)

// AddrInfo is a small struct used to pass around a peer with
// a set of addresses (and later, keys?).
// Deprecated: use github.com/libp2p/go-libp2p/core/peer.AddrInfo instead
type AddrInfo = peer.AddrInfo

// Deprecated: use github.com/libp2p/go-libp2p/core/peer.ErrInvalidAddr instead
var ErrInvalidAddr = peer.ErrInvalidAddr

// AddrInfosFromP2pAddrs converts a set of Multiaddrs to a set of AddrInfos.
// Deprecated: use github.com/libp2p/go-libp2p/core/peer.AddrInfosFromP2pAddrs instead
func AddrInfosFromP2pAddrs(maddrs ...ma.Multiaddr) ([]AddrInfo, error) {
	return peer.AddrInfosFromP2pAddrs(maddrs...)
}

// SplitAddr splits a p2p Multiaddr into a transport multiaddr and a peer ID.
//
// * Returns a nil transport if the address only contains a /p2p part.
// * Returns a empty peer ID if the address doesn't contain a /p2p part.
// Deprecated: use github.com/libp2p/go-libp2p/core/peer.SplitAddr instead
func SplitAddr(m ma.Multiaddr) (transport ma.Multiaddr, id ID) {
	return peer.SplitAddr(m)
}

// AddrInfoFromString builds an AddrInfo from the string representation of a Multiaddr
// Deprecated: use github.com/libp2p/go-libp2p/core/peer.AddrInfoFromString instead
func AddrInfoFromString(s string) (*AddrInfo, error) {
	return peer.AddrInfoFromString(s)
}

// AddrInfoFromP2pAddr converts a Multiaddr to an AddrInfo.
// Deprecated: use github.com/libp2p/go-libp2p/core/peer.AddrInfoFromP2pAddr instead
func AddrInfoFromP2pAddr(m ma.Multiaddr) (*AddrInfo, error) {
	return peer.AddrInfoFromP2pAddr(m)
}

// AddrInfoToP2pAddrs converts an AddrInfo to a list of Multiaddrs.
// Deprecated: use github.com/libp2p/go-libp2p/core/peer.AddrInfoToP2pAddrs instead
func AddrInfoToP2pAddrs(pi *AddrInfo) ([]ma.Multiaddr, error) {
	return peer.AddrInfoToP2pAddrs(pi)
}

// AddrInfosToIDs extracts the peer IDs from the passed AddrInfos and returns them in-order.
// Deprecated: use github.com/libp2p/go-libp2p/core/peer.AddrInfosToIDs instead
func AddrInfosToIDs(pis []AddrInfo) []ID {
	return peer.AddrInfosToIDs(pis)
}
