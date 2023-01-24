package yamux

import (
	"io"
	"math"
	"net"

	"github.com/libp2p/go-libp2p/core/network"

	"github.com/libp2p/go-yamux/v3"
)

var DefaultTransport *Transport

func init() {
	config := yamux.DefaultConfig()
	// We've bumped this to 16MiB as this critically limits throughput.
	//
	// 1MiB means a best case of 10MiB/s (83.89Mbps) on a connection with
	// 100ms latency. The default gave us 2.4MiB *best case* which was
	// totally unacceptable.
	config.MaxStreamWindowSize = uint32(16 * 1024 * 1024)
	// don't spam
	config.LogOutput = io.Discard
	// We always run over a security transport that buffers internally
	// (i.e., uses a block cipher).
	config.ReadBufSize = 0
	// Effectively disable the incoming streams limit.
	// This is now dynamically limited by the resource manager.
	config.MaxIncomingStreams = math.MaxUint32
	DefaultTransport = (*Transport)(config)
}

// Transport implements mux.Multiplexer that constructs
// yamux-backed muxed connections.
type Transport yamux.Config

var _ network.Multiplexer = &Transport{}

func (t *Transport) NewConn(nc net.Conn, isServer bool, scope network.PeerScope) (network.MuxedConn, error) {
	var s *yamux.Session
	var err error
	if isServer {
		s, err = yamux.Server(nc, t.Config(), scope)
	} else {
		s, err = yamux.Client(nc, t.Config(), scope)
	}
	if err != nil {
		return nil, err
	}
	return NewMuxedConn(s), nil
}

func (t *Transport) Config() *yamux.Config {
	return (*yamux.Config)(t)
}
