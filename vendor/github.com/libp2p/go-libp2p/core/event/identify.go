package event

import "github.com/libp2p/go-libp2p/core/peer"

// EvtPeerIdentificationCompleted is emitted when the initial identification round for a peer is completed.
type EvtPeerIdentificationCompleted struct {
	// Peer is the ID of the peer whose identification succeeded.
	Peer peer.ID
}

// EvtPeerIdentificationFailed is emitted when the initial identification round for a peer failed.
type EvtPeerIdentificationFailed struct {
	// Peer is the ID of the peer whose identification failed.
	Peer peer.ID
	// Reason is the reason why identification failed.
	Reason error
}
