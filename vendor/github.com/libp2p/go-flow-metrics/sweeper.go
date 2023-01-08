package flow

import (
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/benbjohnson/clock"
)

// IdleRate the rate at which we declare a meter idle (and stop tracking it
// until it's re-registered).
//
// The default ensures that 1 event every ~30s will keep the meter from going
// idle.
var IdleRate = 1e-13

// Alpha for EWMA of 1s
var alpha = 1 - math.Exp(-1.0)

// The global sweeper.
var globalSweeper sweeper

var cl = clock.New()

// SetClock sets a clock to use in the sweeper.
// This will probably only ever be useful for testing purposes.
func SetClock(c clock.Clock) {
	cl = c
}

type sweeper struct {
	sweepOnce sync.Once

	snapshotMu   sync.RWMutex
	meters       []*Meter
	activeMeters int

	lastUpdateTime  time.Time
	registerChannel chan *Meter
}

func (sw *sweeper) start() {
	sw.registerChannel = make(chan *Meter, 16)
	go sw.run()
}

func (sw *sweeper) run() {
	for m := range sw.registerChannel {
		sw.register(m)
		sw.runActive()
	}
}

func (sw *sweeper) register(m *Meter) {
	if m.registered {
		// registered twice, move on.
		return
	}
	m.registered = true
	sw.meters = append(sw.meters, m)
}

func (sw *sweeper) runActive() {
	ticker := cl.Ticker(time.Second)
	defer ticker.Stop()

	sw.lastUpdateTime = cl.Now()
	for len(sw.meters) > 0 {
		// Scale back allocation.
		if len(sw.meters)*2 < cap(sw.meters) {
			newMeters := make([]*Meter, len(sw.meters))
			copy(newMeters, sw.meters)
			sw.meters = newMeters
		}

		select {
		case <-ticker.C:
			sw.update()
		case m := <-sw.registerChannel:
			sw.register(m)
		}
	}
	sw.meters = nil
	// Till next time.
}

func (sw *sweeper) update() {
	sw.snapshotMu.Lock()
	defer sw.snapshotMu.Unlock()

	now := cl.Now()
	tdiff := now.Sub(sw.lastUpdateTime)
	if tdiff <= 0 {
		return
	}
	sw.lastUpdateTime = now
	timeMultiplier := float64(time.Second) / float64(tdiff)

	// Calculate the bandwidth for all active meters.
	for i, m := range sw.meters[:sw.activeMeters] {
		total := atomic.LoadUint64(&m.accumulator)
		diff := total - m.snapshot.Total
		instant := timeMultiplier * float64(diff)

		if diff > 0 {
			m.snapshot.LastUpdate = now
		}

		if m.snapshot.Rate == 0 {
			m.snapshot.Rate = instant
		} else {
			m.snapshot.Rate += alpha * (instant - m.snapshot.Rate)
		}
		m.snapshot.Total = total

		// This is equivalent to one zeros, then one, then 30 zeros.
		// We'll consider that to be "idle".
		if m.snapshot.Rate > IdleRate {
			continue
		}

		// Ok, so we are idle...

		// Mark this as idle by zeroing the accumulator.
		swappedTotal := atomic.SwapUint64(&m.accumulator, 0)

		// So..., are we really idle?
		if swappedTotal > total {
			// Not so idle...
			// Now we need to make sure this gets re-registered.

			// First, add back what we removed. If we can do this
			// fast enough, we can put it back before anyone
			// notices.
			currentTotal := atomic.AddUint64(&m.accumulator, swappedTotal)

			// Did we make it?
			if currentTotal == swappedTotal {
				// Yes! Nobody noticed, move along.
				continue
			}
			// No. Someone noticed and will (or has) put back into
			// the registration channel.
			//
			// Remove the snapshot total, it'll get added back on
			// registration.
			//
			// `^uint64(total - 1)` is the two's complement of
			// `total`. It's the "correct" way to subtract
			// atomically in go.
			atomic.AddUint64(&m.accumulator, ^uint64(m.snapshot.Total-1))
		}

		// Reset the rate, keep the total.
		m.registered = false
		m.snapshot.Rate = 0
		sw.meters[i] = nil
	}

	// Re-add the total to all the newly active accumulators and set the snapshot to the total.
	// 1. We don't do this on register to avoid having to take the snapshot lock.
	// 2. We skip calculating the bandwidth for this round so we get an _accurate_ bandwidth calculation.
	for _, m := range sw.meters[sw.activeMeters:] {
		total := atomic.AddUint64(&m.accumulator, m.snapshot.Total)
		if total > m.snapshot.Total {
			m.snapshot.LastUpdate = now
		}
		m.snapshot.Total = total
	}

	// compress and trim the meter list
	var newLen int
	for _, m := range sw.meters {
		if m != nil {
			sw.meters[newLen] = m
			newLen++
		}
	}

	sw.meters = sw.meters[:newLen]

	// Finally, mark all meters still in the list as "active".
	sw.activeMeters = len(sw.meters)
}

func (sw *sweeper) Register(m *Meter) {
	sw.sweepOnce.Do(sw.start)
	sw.registerChannel <- m
}
