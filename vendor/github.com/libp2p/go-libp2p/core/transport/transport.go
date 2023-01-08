// Package transport provides the Transport interface, which represents
// the devices and network protocols used to send and receive data.
package transport

import (
	"context"
	"net"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
)

// A CapableConn represents a connection that has offers the basic
// capabilities required by libp2p: stream multiplexing, encryption and
// peer authentication.
//
// These capabilities may be natively provided by the transport, or they
// may be shimmed via the "connection upgrade" process, which converts a
// "raw" network connection into one that supports such capabilities by
// layering an encryption channel and a stream multiplexer.
//
// CapableConn provides accessors for the local and remote multiaddrs used to
// establish the connection and an accessor for the underlying Transport.
type CapableConn interface {
	network.MuxedConn
	network.ConnSecurity
	network.ConnMultiaddrs
	network.ConnScoper

	// Transport returns the transport to which this connection belongs.
	Transport() Transport
}

// Transport represents any device by which you can connect to and accept
// connections from other peers.
//
// The Transport interface allows you to open connections to other peers
// by dialing them, and also lets you listen for incoming connections.
//
// Connections returned by Dial and passed into Listeners are of type
// CapableConn, which means that they have been upgraded to support
// stream multiplexing and connection security (encryption and authentication).
//
// If a transport implements `io.Closer` (optional), libp2p will call `Close` on
// shutdown. NOTE: `Dial` and `Listen` may be called after or concurrently with
// `Close`.
//
// For a conceptual overview, see https://docs.libp2p.io/concepts/transport/
type Transport interface {
	// Dial dials a remote peer. It should try to reuse local listener
	// addresses if possible but it may choose not to.
	Dial(ctx context.Context, raddr ma.Multiaddr, p peer.ID) (CapableConn, error)

	// CanDial returns true if this transport knows how to dial the given
	// multiaddr.
	//
	// Returning true does not guarantee that dialing this multiaddr will
	// succeed. This function should *only* be used to preemptively filter
	// out addresses that we can't dial.
	CanDial(addr ma.Multiaddr) bool

	// Listen listens on the passed multiaddr.
	Listen(laddr ma.Multiaddr) (Listener, error)

	// Protocol returns the set of protocols handled by this transport.
	//
	// See the Network interface for an explanation of how this is used.
	Protocols() []int

	// Proxy returns true if this is a proxy transport.
	//
	// See the Network interface for an explanation of how this is used.
	// TODO: Make this a part of the go-multiaddr protocol instead?
	Proxy() bool
}

// Listener is an interface closely resembling the net.Listener interface. The
// only real difference is that Accept() returns Conn's of the type in this
// package, and also exposes a Multiaddr method as opposed to a regular Addr
// method
type Listener interface {
	Accept() (CapableConn, error)
	Close() error
	Addr() net.Addr
	Multiaddr() ma.Multiaddr
}

// TransportNetwork is an inet.Network with methods for managing transports.
type TransportNetwork interface {
	network.Network

	// AddTransport adds a transport to this Network.
	//
	// When dialing, this Network will iterate over the protocols in the
	// remote multiaddr and pick the first protocol registered with a proxy
	// transport, if any. Otherwise, it'll pick the transport registered to
	// handle the last protocol in the multiaddr.
	//
	// When listening, this Network will iterate over the protocols in the
	// local multiaddr and pick the *last* protocol registered with a proxy
	// transport, if any. Otherwise, it'll pick the transport registered to
	// handle the last protocol in the multiaddr.
	AddTransport(t Transport) error
}

// Upgrader is a multistream upgrader that can upgrade an underlying connection
// to a full transport connection (secure and multiplexed).
type Upgrader interface {
	// UpgradeListener upgrades the passed multiaddr-net listener into a full libp2p-transport listener.
	UpgradeListener(Transport, manet.Listener) Listener
	// Upgrade upgrades the multiaddr/net connection into a full libp2p-transport connection.
	Upgrade(ctx context.Context, t Transport, maconn manet.Conn, dir network.Direction, p peer.ID, scope network.ConnManagementScope) (CapableConn, error)
}
