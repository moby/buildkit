package libp2pquic

import (
	"errors"

	"github.com/libp2p/go-libp2p/core/network"

	"github.com/lucas-clemente/quic-go"
)

const (
	reset quic.StreamErrorCode = 0
)

type stream struct {
	quic.Stream
}

var _ network.MuxedStream = &stream{}

func (s *stream) Read(b []byte) (n int, err error) {
	n, err = s.Stream.Read(b)
	if err != nil && errors.Is(err, &quic.StreamError{}) {
		err = network.ErrReset
	}
	return n, err
}

func (s *stream) Write(b []byte) (n int, err error) {
	n, err = s.Stream.Write(b)
	if err != nil && errors.Is(err, &quic.StreamError{}) {
		err = network.ErrReset
	}
	return n, err
}

func (s *stream) Reset() error {
	s.Stream.CancelRead(reset)
	s.Stream.CancelWrite(reset)
	return nil
}

func (s *stream) Close() error {
	s.Stream.CancelRead(reset)
	return s.Stream.Close()
}

func (s *stream) CloseRead() error {
	s.Stream.CancelRead(reset)
	return nil
}

func (s *stream) CloseWrite() error {
	return s.Stream.Close()
}
