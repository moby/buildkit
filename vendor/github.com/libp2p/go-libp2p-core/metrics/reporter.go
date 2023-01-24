// Package metrics provides metrics collection and reporting interfaces for libp2p.
package metrics

import (
	"github.com/libp2p/go-libp2p/core/metrics"
)

// Stats represents a point-in-time snapshot of bandwidth metrics.
//
// The TotalIn and TotalOut fields record cumulative bytes sent / received.
// The RateIn and RateOut fields record bytes sent / received per second.
// Deprecated: use github.com/libp2p/go-libp2p/core/metrics.Stats instead
type Stats = metrics.Stats

// Reporter provides methods for logging and retrieving metrics.
// Deprecated: use github.com/libp2p/go-libp2p/core/metrics.Reporter instead
type Reporter = metrics.Reporter
