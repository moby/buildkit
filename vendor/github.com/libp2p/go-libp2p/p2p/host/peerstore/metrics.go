package peerstore

import (
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

// LatencyEWMASmoothing governs the decay of the EWMA (the speed
// at which it changes). This must be a normalized (0-1) value.
// 1 is 100% change, 0 is no change.
var LatencyEWMASmoothing = 0.1

type metrics struct {
	mutex  sync.RWMutex
	latmap map[peer.ID]time.Duration
}

func NewMetrics() *metrics {
	return &metrics{
		latmap: make(map[peer.ID]time.Duration),
	}
}

// RecordLatency records a new latency measurement
func (m *metrics) RecordLatency(p peer.ID, next time.Duration) {
	nextf := float64(next)
	s := LatencyEWMASmoothing
	if s > 1 || s < 0 {
		s = 0.1 // ignore the knob. it's broken. look, it jiggles.
	}

	m.mutex.Lock()
	ewma, found := m.latmap[p]
	ewmaf := float64(ewma)
	if !found {
		m.latmap[p] = next // when no data, just take it as the mean.
	} else {
		nextf = ((1.0 - s) * ewmaf) + (s * nextf)
		m.latmap[p] = time.Duration(nextf)
	}
	m.mutex.Unlock()
}

// LatencyEWMA returns an exponentially-weighted moving avg.
// of all measurements of a peer's latency.
func (m *metrics) LatencyEWMA(p peer.ID) time.Duration {
	m.mutex.RLock()
	defer m.mutex.RUnlock()
	return m.latmap[p]
}

func (m *metrics) RemovePeer(p peer.ID) {
	m.mutex.Lock()
	delete(m.latmap, p)
	m.mutex.Unlock()
}
