package flow

import (
	"fmt"
	"sync/atomic"
	"time"
)

// Snapshot is a rate/total snapshot.
type Snapshot struct {
	Rate       float64
	Total      uint64
	LastUpdate time.Time
}

// NewMeter returns a new Meter with the correct idle time.
//
// While zero-value Meters can be used, their "last update" time will start at
// the program start instead of when the meter was created.
func NewMeter() *Meter {
	return &Meter{
		snapshot: Snapshot{
			LastUpdate: time.Now(),
		},
	}
}

func (s Snapshot) String() string {
	return fmt.Sprintf("%d (%f/s)", s.Total, s.Rate)
}

// Meter is a meter for monitoring a flow.
type Meter struct {
	accumulator uint64

	// managed by the sweeper loop.
	registered bool

	// Take lock.
	snapshot Snapshot
}

// Mark updates the total.
func (m *Meter) Mark(count uint64) {
	if count > 0 && atomic.AddUint64(&m.accumulator, count) == count {
		// The accumulator is 0 so we probably need to register. We may
		// already _be_ registered however, if we are, the registration
		// loop will notice that `m.registered` is set and ignore us.
		globalSweeper.Register(m)
	}
}

// Snapshot gets a snapshot of the total and rate.
func (m *Meter) Snapshot() Snapshot {
	globalSweeper.snapshotMu.RLock()
	defer globalSweeper.snapshotMu.RUnlock()
	return m.snapshot
}

// Reset sets accumulator, total and rate to zero.
func (m *Meter) Reset() {
	globalSweeper.snapshotMu.Lock()
	atomic.StoreUint64(&m.accumulator, 0)
	m.snapshot.Rate = 0
	m.snapshot.Total = 0
	globalSweeper.snapshotMu.Unlock()
}

func (m *Meter) String() string {
	return m.Snapshot().String()
}
