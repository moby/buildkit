// Package metrics provides metrics collection and reporting interfaces for libp2p.
package metrics

import (
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
)

// Stats represents a point-in-time snapshot of bandwidth metrics.
//
// The TotalIn and TotalOut fields record cumulative bytes sent / received.
// The RateIn and RateOut fields record bytes sent / received per second.
type Stats struct {
	TotalIn  int64
	TotalOut int64
	RateIn   float64
	RateOut  float64
}

// Reporter provides methods for logging and retrieving metrics.
type Reporter interface {
	LogSentMessage(int64)
	LogRecvMessage(int64)
	LogSentMessageStream(int64, protocol.ID, peer.ID)
	LogRecvMessageStream(int64, protocol.ID, peer.ID)
	GetBandwidthForPeer(peer.ID) Stats
	GetBandwidthForProtocol(protocol.ID) Stats
	GetBandwidthTotals() Stats
	GetBandwidthByPeer() map[peer.ID]Stats
	GetBandwidthByProtocol() map[protocol.ID]Stats
}
