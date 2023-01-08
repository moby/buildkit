package swarm

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	ic "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/transport"

	ma "github.com/multiformats/go-multiaddr"
)

// TODO: Put this elsewhere.

// ErrConnClosed is returned when operating on a closed connection.
var ErrConnClosed = errors.New("connection closed")

// Conn is the connection type used by swarm. In general, you won't use this
// type directly.
type Conn struct {
	id    uint64
	conn  transport.CapableConn
	swarm *Swarm

	closeOnce sync.Once
	err       error

	notifyLk sync.Mutex

	streams struct {
		sync.Mutex
		m map[*Stream]struct{}
	}

	stat network.ConnStats
}

var _ network.Conn = &Conn{}

func (c *Conn) ID() string {
	// format: <first 10 chars of peer id>-<global conn ordinal>
	return fmt.Sprintf("%s-%d", c.RemotePeer().Pretty()[0:10], c.id)
}

// Close closes this connection.
//
// Note: This method won't wait for the close notifications to finish as that
// would create a deadlock when called from an open notification (because all
// open notifications must finish before we can fire off the close
// notifications).
func (c *Conn) Close() error {
	c.closeOnce.Do(c.doClose)
	return c.err
}

func (c *Conn) doClose() {
	c.swarm.removeConn(c)

	// Prevent new streams from opening.
	c.streams.Lock()
	streams := c.streams.m
	c.streams.m = nil
	c.streams.Unlock()

	c.err = c.conn.Close()

	// This is just for cleaning up state. The connection has already been closed.
	// We *could* optimize this but it really isn't worth it.
	for s := range streams {
		s.Reset()
	}

	// do this in a goroutine to avoid deadlocking if we call close in an open notification.
	go func() {
		// prevents us from issuing close notifications before finishing the open notifications
		c.notifyLk.Lock()
		defer c.notifyLk.Unlock()

		c.swarm.notifyAll(func(f network.Notifiee) {
			f.Disconnected(c.swarm, c)
		})
		c.swarm.refs.Done() // taken in Swarm.addConn
	}()
}

func (c *Conn) removeStream(s *Stream) {
	c.streams.Lock()
	c.stat.NumStreams--
	delete(c.streams.m, s)
	c.streams.Unlock()
	s.scope.Done()
}

// listens for new streams.
//
// The caller must take a swarm ref before calling. This function decrements the
// swarm ref count.
func (c *Conn) start() {
	go func() {
		defer c.swarm.refs.Done()
		defer c.Close()

		for {
			ts, err := c.conn.AcceptStream()
			if err != nil {
				return
			}
			scope, err := c.swarm.ResourceManager().OpenStream(c.RemotePeer(), network.DirInbound)
			if err != nil {
				ts.Reset()
				continue
			}
			c.swarm.refs.Add(1)
			go func() {
				s, err := c.addStream(ts, network.DirInbound, scope)

				// Don't defer this. We don't want to block
				// swarm shutdown on the connection handler.
				c.swarm.refs.Done()

				// We only get an error here when the swarm is closed or closing.
				if err != nil {
					return
				}

				if h := c.swarm.StreamHandler(); h != nil {
					h(s)
				}
			}()
		}
	}()
}

func (c *Conn) String() string {
	return fmt.Sprintf(
		"<swarm.Conn[%T] %s (%s) <-> %s (%s)>",
		c.conn.Transport(),
		c.conn.LocalMultiaddr(),
		c.conn.LocalPeer().Pretty(),
		c.conn.RemoteMultiaddr(),
		c.conn.RemotePeer().Pretty(),
	)
}

// LocalMultiaddr is the Multiaddr on this side
func (c *Conn) LocalMultiaddr() ma.Multiaddr {
	return c.conn.LocalMultiaddr()
}

// LocalPeer is the Peer on our side of the connection
func (c *Conn) LocalPeer() peer.ID {
	return c.conn.LocalPeer()
}

// RemoteMultiaddr is the Multiaddr on the remote side
func (c *Conn) RemoteMultiaddr() ma.Multiaddr {
	return c.conn.RemoteMultiaddr()
}

// RemotePeer is the Peer on the remote side
func (c *Conn) RemotePeer() peer.ID {
	return c.conn.RemotePeer()
}

// LocalPrivateKey is the public key of the peer on this side
func (c *Conn) LocalPrivateKey() ic.PrivKey {
	return c.conn.LocalPrivateKey()
}

// RemotePublicKey is the public key of the peer on the remote side
func (c *Conn) RemotePublicKey() ic.PubKey {
	return c.conn.RemotePublicKey()
}

// Stat returns metadata pertaining to this connection
func (c *Conn) Stat() network.ConnStats {
	c.streams.Lock()
	defer c.streams.Unlock()
	return c.stat
}

// NewStream returns a new Stream from this connection
func (c *Conn) NewStream(ctx context.Context) (network.Stream, error) {
	if c.Stat().Transient {
		if useTransient, _ := network.GetUseTransient(ctx); !useTransient {
			return nil, network.ErrTransientConn
		}
	}

	scope, err := c.swarm.ResourceManager().OpenStream(c.RemotePeer(), network.DirOutbound)
	if err != nil {
		return nil, err
	}
	ts, err := c.conn.OpenStream(ctx)
	if err != nil {
		scope.Done()
		return nil, err
	}
	return c.addStream(ts, network.DirOutbound, scope)
}

func (c *Conn) addStream(ts network.MuxedStream, dir network.Direction, scope network.StreamManagementScope) (*Stream, error) {
	c.streams.Lock()
	// Are we still online?
	if c.streams.m == nil {
		c.streams.Unlock()
		scope.Done()
		ts.Reset()
		return nil, ErrConnClosed
	}

	// Wrap and register the stream.
	s := &Stream{
		stream: ts,
		conn:   c,
		scope:  scope,
		stat: network.Stats{
			Direction: dir,
			Opened:    time.Now(),
		},
		id: atomic.AddUint64(&c.swarm.nextStreamID, 1),
	}
	c.stat.NumStreams++
	c.streams.m[s] = struct{}{}

	// Released once the stream disconnect notifications have finished
	// firing (in Swarm.remove).
	c.swarm.refs.Add(1)

	c.streams.Unlock()
	return s, nil
}

// GetStreams returns the streams associated with this connection.
func (c *Conn) GetStreams() []network.Stream {
	c.streams.Lock()
	defer c.streams.Unlock()
	streams := make([]network.Stream, 0, len(c.streams.m))
	for s := range c.streams.m {
		streams = append(streams, s)
	}
	return streams
}

func (c *Conn) Scope() network.ConnScope {
	return c.conn.Scope()
}
