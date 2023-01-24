package rcmgr

import (
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
)

// MetricsReporter is an interface for collecting metrics from resource manager actions
type MetricsReporter interface {
	// AllowConn is invoked when opening a connection is allowed
	AllowConn(dir network.Direction, usefd bool)
	// BlockConn is invoked when opening a connection is blocked
	BlockConn(dir network.Direction, usefd bool)

	// AllowStream is invoked when opening a stream is allowed
	AllowStream(p peer.ID, dir network.Direction)
	// BlockStream is invoked when opening a stream is blocked
	BlockStream(p peer.ID, dir network.Direction)

	// AllowPeer is invoked when attaching ac onnection to a peer is allowed
	AllowPeer(p peer.ID)
	// BlockPeer is invoked when attaching ac onnection to a peer is blocked
	BlockPeer(p peer.ID)

	// AllowProtocol is invoked when setting the protocol for a stream is allowed
	AllowProtocol(proto protocol.ID)
	// BlockProtocol is invoked when setting the protocol for a stream is blocked
	BlockProtocol(proto protocol.ID)
	// BlockProtocolPeer is invoked when setting the protocol for a stream is blocked at the per protocol peer scope
	BlockProtocolPeer(proto protocol.ID, p peer.ID)

	// AllowService is invoked when setting the protocol for a stream is allowed
	AllowService(svc string)
	// BlockService is invoked when setting the protocol for a stream is blocked
	BlockService(svc string)
	// BlockServicePeer is invoked when setting the service for a stream is blocked at the per service peer scope
	BlockServicePeer(svc string, p peer.ID)

	// AllowMemory is invoked when a memory reservation is allowed
	AllowMemory(size int)
	// BlockMemory is invoked when a memory reservation is blocked
	BlockMemory(size int)
}

type metrics struct {
	reporter MetricsReporter
}

// WithMetrics is a resource manager option to enable metrics collection
func WithMetrics(reporter MetricsReporter) Option {
	return func(r *resourceManager) error {
		r.metrics = &metrics{reporter: reporter}
		return nil
	}
}

func (m *metrics) AllowConn(dir network.Direction, usefd bool) {
	if m == nil {
		return
	}

	m.reporter.AllowConn(dir, usefd)
}

func (m *metrics) BlockConn(dir network.Direction, usefd bool) {
	if m == nil {
		return
	}

	m.reporter.BlockConn(dir, usefd)
}

func (m *metrics) AllowStream(p peer.ID, dir network.Direction) {
	if m == nil {
		return
	}

	m.reporter.AllowStream(p, dir)
}

func (m *metrics) BlockStream(p peer.ID, dir network.Direction) {
	if m == nil {
		return
	}

	m.reporter.BlockStream(p, dir)
}

func (m *metrics) AllowPeer(p peer.ID) {
	if m == nil {
		return
	}

	m.reporter.AllowPeer(p)
}

func (m *metrics) BlockPeer(p peer.ID) {
	if m == nil {
		return
	}

	m.reporter.BlockPeer(p)
}

func (m *metrics) AllowProtocol(proto protocol.ID) {
	if m == nil {
		return
	}

	m.reporter.AllowProtocol(proto)
}

func (m *metrics) BlockProtocol(proto protocol.ID) {
	if m == nil {
		return
	}

	m.reporter.BlockProtocol(proto)
}

func (m *metrics) BlockProtocolPeer(proto protocol.ID, p peer.ID) {
	if m == nil {
		return
	}

	m.reporter.BlockProtocolPeer(proto, p)
}

func (m *metrics) AllowService(svc string) {
	if m == nil {
		return
	}

	m.reporter.AllowService(svc)
}

func (m *metrics) BlockService(svc string) {
	if m == nil {
		return
	}

	m.reporter.BlockService(svc)
}

func (m *metrics) BlockServicePeer(svc string, p peer.ID) {
	if m == nil {
		return
	}

	m.reporter.BlockServicePeer(svc, p)
}

func (m *metrics) AllowMemory(size int) {
	if m == nil {
		return
	}

	m.reporter.AllowMemory(size)
}

func (m *metrics) BlockMemory(size int) {
	if m == nil {
		return
	}

	m.reporter.BlockMemory(size)
}
