package client

import (
	"fmt"
	"net"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
)

// HopTagWeight is the connection manager weight for connections carrying relay hop streams
var HopTagWeight = 5

type statLimitDuration struct{}
type statLimitData struct{}

var (
	StatLimitDuration = statLimitDuration{}
	StatLimitData     = statLimitData{}
)

type Conn struct {
	stream network.Stream
	remote peer.AddrInfo
	stat   network.ConnStats

	client *Client
}

type NetAddr struct {
	Relay  string
	Remote string
}

var _ net.Addr = (*NetAddr)(nil)

func (n *NetAddr) Network() string {
	return "libp2p-circuit-relay"
}

func (n *NetAddr) String() string {
	return fmt.Sprintf("relay[%s-%s]", n.Remote, n.Relay)
}

// Conn interface
var _ manet.Conn = (*Conn)(nil)

func (c *Conn) Close() error {
	c.untagHop()
	return c.stream.Reset()
}

func (c *Conn) Read(buf []byte) (int, error) {
	return c.stream.Read(buf)
}

func (c *Conn) Write(buf []byte) (int, error) {
	return c.stream.Write(buf)
}

func (c *Conn) SetDeadline(t time.Time) error {
	return c.stream.SetDeadline(t)
}

func (c *Conn) SetReadDeadline(t time.Time) error {
	return c.stream.SetReadDeadline(t)
}

func (c *Conn) SetWriteDeadline(t time.Time) error {
	return c.stream.SetWriteDeadline(t)
}

// TODO: is it okay to cast c.Conn().RemotePeer() into a multiaddr? might be "user input"
func (c *Conn) RemoteMultiaddr() ma.Multiaddr {
	// TODO: We should be able to do this directly without converting to/from a string.
	relayAddr, err := ma.NewComponent(
		ma.ProtocolWithCode(ma.P_P2P).Name,
		c.stream.Conn().RemotePeer().Pretty(),
	)
	if err != nil {
		panic(err)
	}
	return ma.Join(c.stream.Conn().RemoteMultiaddr(), relayAddr, circuitAddr)
}

func (c *Conn) LocalMultiaddr() ma.Multiaddr {
	return c.stream.Conn().LocalMultiaddr()
}

func (c *Conn) LocalAddr() net.Addr {
	na, err := manet.ToNetAddr(c.stream.Conn().LocalMultiaddr())
	if err != nil {
		log.Error("failed to convert local multiaddr to net addr:", err)
		return nil
	}
	return na
}

func (c *Conn) RemoteAddr() net.Addr {
	return &NetAddr{
		Relay:  c.stream.Conn().RemotePeer().Pretty(),
		Remote: c.remote.ID.Pretty(),
	}
}

// ConnStat interface
var _ network.ConnStat = (*Conn)(nil)

func (c *Conn) Stat() network.ConnStats {
	return c.stat
}

// tagHop tags the underlying relay connection so that it can be (somewhat) protected from the
// connection manager as it is an important connection that proxies other connections.
// This is handled here so that the user code doesnt need to bother with this and avoid
// clown shoes situations where a high value peer connection is behind a relayed connection and it is
// implicitly because the connection manager closed the underlying relay connection.
func (c *Conn) tagHop() {
	c.client.mx.Lock()
	defer c.client.mx.Unlock()

	p := c.stream.Conn().RemotePeer()
	c.client.hopCount[p]++
	if c.client.hopCount[p] == 1 {
		c.client.host.ConnManager().TagPeer(p, "relay-hop-stream", HopTagWeight)
	}
}

// untagHop removes the relay-hop-stream tag if necessary; it is invoked when a relayed connection
// is closed.
func (c *Conn) untagHop() {
	c.client.mx.Lock()
	defer c.client.mx.Unlock()

	p := c.stream.Conn().RemotePeer()
	c.client.hopCount[p]--
	if c.client.hopCount[p] == 0 {
		c.client.host.ConnManager().UntagPeer(p, "relay-hop-stream")
		delete(c.client.hopCount, p)
	}
}
