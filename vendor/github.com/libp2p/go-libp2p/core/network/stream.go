package network

import (
	"github.com/libp2p/go-libp2p/core/protocol"
)

// Stream represents a bidirectional channel between two agents in
// a libp2p network. "agent" is as granular as desired, potentially
// being a "request -> reply" pair, or whole protocols.
//
// Streams are backed by a multiplexer underneath the hood.
type Stream interface {
	MuxedStream

	// ID returns an identifier that uniquely identifies this Stream within this
	// host, during this run. Stream IDs may repeat across restarts.
	ID() string

	Protocol() protocol.ID
	SetProtocol(id protocol.ID) error

	// Stat returns metadata pertaining to this stream.
	Stat() Stats

	// Conn returns the connection this stream is part of.
	Conn() Conn

	// Scope returns the user's view of this stream's resource scope
	Scope() StreamScope
}
