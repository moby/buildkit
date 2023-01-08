package gostream

import "github.com/libp2p/go-libp2p/core/peer"

// addr implements net.Addr and holds a libp2p peer ID.
type addr struct{ id peer.ID }

// Network returns the name of the network that this address belongs to
// (libp2p).
func (a *addr) Network() string { return Network }

// String returns the peer ID of this address in string form
// (B58-encoded).
func (a *addr) String() string { return a.id.String() }
