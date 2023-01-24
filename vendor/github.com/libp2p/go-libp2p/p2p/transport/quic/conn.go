package libp2pquic

import (
	"context"

	ic "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	tpt "github.com/libp2p/go-libp2p/core/transport"

	"github.com/lucas-clemente/quic-go"
	ma "github.com/multiformats/go-multiaddr"
)

type conn struct {
	quicConn  quic.Connection
	pconn     *reuseConn
	transport *transport
	scope     network.ConnManagementScope

	localPeer      peer.ID
	privKey        ic.PrivKey
	localMultiaddr ma.Multiaddr

	remotePeerID    peer.ID
	remotePubKey    ic.PubKey
	remoteMultiaddr ma.Multiaddr
}

var _ tpt.CapableConn = &conn{}

// Close closes the connection.
// It must be called even if the peer closed the connection in order for
// garbage collection to properly work in this package.
func (c *conn) Close() error {
	c.transport.removeConn(c.quicConn)
	err := c.quicConn.CloseWithError(0, "")
	c.pconn.DecreaseCount()
	c.scope.Done()
	return err
}

// IsClosed returns whether a connection is fully closed.
func (c *conn) IsClosed() bool {
	return c.quicConn.Context().Err() != nil
}

func (c *conn) allowWindowIncrease(size uint64) bool {
	return c.scope.ReserveMemory(int(size), network.ReservationPriorityMedium) == nil
}

// OpenStream creates a new stream.
func (c *conn) OpenStream(ctx context.Context) (network.MuxedStream, error) {
	qstr, err := c.quicConn.OpenStreamSync(ctx)
	return &stream{Stream: qstr}, err
}

// AcceptStream accepts a stream opened by the other side.
func (c *conn) AcceptStream() (network.MuxedStream, error) {
	qstr, err := c.quicConn.AcceptStream(context.Background())
	return &stream{Stream: qstr}, err
}

// LocalPeer returns our peer ID
func (c *conn) LocalPeer() peer.ID {
	return c.localPeer
}

// LocalPrivateKey returns our private key
func (c *conn) LocalPrivateKey() ic.PrivKey {
	return c.privKey
}

// RemotePeer returns the peer ID of the remote peer.
func (c *conn) RemotePeer() peer.ID {
	return c.remotePeerID
}

// RemotePublicKey returns the public key of the remote peer.
func (c *conn) RemotePublicKey() ic.PubKey {
	return c.remotePubKey
}

// LocalMultiaddr returns the local Multiaddr associated
func (c *conn) LocalMultiaddr() ma.Multiaddr {
	return c.localMultiaddr
}

// RemoteMultiaddr returns the remote Multiaddr associated
func (c *conn) RemoteMultiaddr() ma.Multiaddr {
	return c.remoteMultiaddr
}

func (c *conn) Transport() tpt.Transport {
	return c.transport
}

func (c *conn) Scope() network.ConnScope {
	return c.scope
}
