package event

import (
	peer "github.com/libp2p/go-libp2p/core/peer"
	protocol "github.com/libp2p/go-libp2p/core/protocol"
)

// EvtPeerProtocolsUpdated should be emitted when a peer we're connected to adds or removes protocols from their stack.
type EvtPeerProtocolsUpdated struct {
	// Peer is the peer whose protocols were updated.
	Peer peer.ID
	// Added enumerates the protocols that were added by this peer.
	Added []protocol.ID
	// Removed enumerates the protocols that were removed by this peer.
	Removed []protocol.ID
}

// EvtLocalProtocolsUpdated should be emitted when stream handlers are attached or detached from the local host.
// For handlers attached with a matcher predicate (host.SetStreamHandlerMatch()), only the protocol ID will be
// included in this event.
type EvtLocalProtocolsUpdated struct {
	// Added enumerates the protocols that were added locally.
	Added []protocol.ID
	// Removed enumerates the protocols that were removed locally.
	Removed []protocol.ID
}
