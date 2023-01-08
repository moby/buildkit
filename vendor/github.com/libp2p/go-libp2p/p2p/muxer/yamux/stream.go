package yamux

import (
	"time"

	"github.com/libp2p/go-libp2p/core/network"

	"github.com/libp2p/go-yamux/v3"
)

// stream implements mux.MuxedStream over yamux.Stream.
type stream yamux.Stream

var _ network.MuxedStream = &stream{}

func (s *stream) Read(b []byte) (n int, err error) {
	n, err = s.yamux().Read(b)
	if err == yamux.ErrStreamReset {
		err = network.ErrReset
	}

	return n, err
}

func (s *stream) Write(b []byte) (n int, err error) {
	n, err = s.yamux().Write(b)
	if err == yamux.ErrStreamReset {
		err = network.ErrReset
	}

	return n, err
}

func (s *stream) Close() error {
	return s.yamux().Close()
}

func (s *stream) Reset() error {
	return s.yamux().Reset()
}

func (s *stream) CloseRead() error {
	return s.yamux().CloseRead()
}

func (s *stream) CloseWrite() error {
	return s.yamux().CloseWrite()
}

func (s *stream) SetDeadline(t time.Time) error {
	return s.yamux().SetDeadline(t)
}

func (s *stream) SetReadDeadline(t time.Time) error {
	return s.yamux().SetReadDeadline(t)
}

func (s *stream) SetWriteDeadline(t time.Time) error {
	return s.yamux().SetWriteDeadline(t)
}

func (s *stream) yamux() *yamux.Stream {
	return (*yamux.Stream)(s)
}
