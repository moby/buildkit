// Deprecated: This package has moved into go-libp2p as a sub-package: github.com/libp2p/go-libp2p/core/metrics.
//
// Package metrics provides metrics collection and reporting interfaces for libp2p.
package metrics

import (
	"github.com/libp2p/go-libp2p/core/metrics"
)

// BandwidthCounter tracks incoming and outgoing data transferred by the local peer.
// Metrics are available for total bandwidth across all peers / protocols, as well
// as segmented by remote peer ID and protocol ID.
// Deprecated: use github.com/libp2p/go-libp2p/core/metrics.BandwidthCounter instead
type BandwidthCounter = metrics.BandwidthCounter

// NewBandwidthCounter creates a new BandwidthCounter.
// Deprecated: use github.com/libp2p/go-libp2p/core/metrics.NewBandwidthCounter instead
func NewBandwidthCounter() *BandwidthCounter {
	return metrics.NewBandwidthCounter()
}
