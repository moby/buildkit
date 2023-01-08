package swarm

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/protocol"
)

// Validate Stream conforms to the go-libp2p-net Stream interface
var _ network.Stream = &Stream{}

// Stream is the stream type used by swarm. In general, you won't use this type
// directly.
type Stream struct {
	id uint64

	stream network.MuxedStream
	conn   *Conn
	scope  network.StreamManagementScope

	closeOnce sync.Once

	protocol atomic.Value

	stat network.Stats
}

func (s *Stream) ID() string {
	// format: <first 10 chars of peer id>-<global conn ordinal>-<global stream ordinal>
	return fmt.Sprintf("%s-%d", s.conn.ID(), s.id)
}

func (s *Stream) String() string {
	return fmt.Sprintf(
		"<swarm.Stream[%s] %s (%s) <-> %s (%s)>",
		s.conn.conn.Transport(),
		s.conn.LocalMultiaddr(),
		s.conn.LocalPeer(),
		s.conn.RemoteMultiaddr(),
		s.conn.RemotePeer(),
	)
}

// Conn returns the Conn associated with this stream, as an network.Conn
func (s *Stream) Conn() network.Conn {
	return s.conn
}

// Read reads bytes from a stream.
func (s *Stream) Read(p []byte) (int, error) {
	n, err := s.stream.Read(p)
	// TODO: push this down to a lower level for better accuracy.
	if s.conn.swarm.bwc != nil {
		s.conn.swarm.bwc.LogRecvMessage(int64(n))
		s.conn.swarm.bwc.LogRecvMessageStream(int64(n), s.Protocol(), s.Conn().RemotePeer())
	}
	return n, err
}

// Write writes bytes to a stream, flushing for each call.
func (s *Stream) Write(p []byte) (int, error) {
	n, err := s.stream.Write(p)
	// TODO: push this down to a lower level for better accuracy.
	if s.conn.swarm.bwc != nil {
		s.conn.swarm.bwc.LogSentMessage(int64(n))
		s.conn.swarm.bwc.LogSentMessageStream(int64(n), s.Protocol(), s.Conn().RemotePeer())
	}
	return n, err
}

// Close closes the stream, closing both ends and freeing all associated
// resources.
func (s *Stream) Close() error {
	err := s.stream.Close()
	s.closeOnce.Do(s.remove)
	return err
}

// Reset resets the stream, signaling an error on both ends and freeing all
// associated resources.
func (s *Stream) Reset() error {
	err := s.stream.Reset()
	s.closeOnce.Do(s.remove)
	return err
}

// CloseWrite closes the stream for writing, flushing all data and sending an EOF.
// This function does not free resources, call Close or Reset when done with the
// stream.
func (s *Stream) CloseWrite() error {
	return s.stream.CloseWrite()
}

// CloseRead closes the stream for reading. This function does not free resources,
// call Close or Reset when done with the stream.
func (s *Stream) CloseRead() error {
	return s.stream.CloseRead()
}

func (s *Stream) remove() {
	s.conn.removeStream(s)
	s.conn.swarm.refs.Done()
}

// Protocol returns the protocol negotiated on this stream (if set).
func (s *Stream) Protocol() protocol.ID {
	// Ignore type error. It means that the protocol is unset.
	p, _ := s.protocol.Load().(protocol.ID)
	return p
}

// SetProtocol sets the protocol for this stream.
//
// This doesn't actually *do* anything other than record the fact that we're
// speaking the given protocol over this stream. It's still up to the user to
// negotiate the protocol. This is usually done by the Host.
func (s *Stream) SetProtocol(p protocol.ID) error {
	if err := s.scope.SetProtocol(p); err != nil {
		return err
	}

	s.protocol.Store(p)
	return nil
}

// SetDeadline sets the read and write deadlines for this stream.
func (s *Stream) SetDeadline(t time.Time) error {
	return s.stream.SetDeadline(t)
}

// SetReadDeadline sets the read deadline for this stream.
func (s *Stream) SetReadDeadline(t time.Time) error {
	return s.stream.SetReadDeadline(t)
}

// SetWriteDeadline sets the write deadline for this stream.
func (s *Stream) SetWriteDeadline(t time.Time) error {
	return s.stream.SetWriteDeadline(t)
}

// Stat returns metadata information for this stream.
func (s *Stream) Stat() network.Stats {
	return s.stat
}

func (s *Stream) Scope() network.StreamScope {
	return s.scope
}
