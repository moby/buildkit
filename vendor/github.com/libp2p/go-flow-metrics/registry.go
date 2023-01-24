package flow

import (
	"sync"
	"time"
)

// MeterRegistry is a registry for named meters.
type MeterRegistry struct {
	meters sync.Map
}

// Get gets (or creates) a meter by name.
func (r *MeterRegistry) Get(name string) *Meter {
	if m, ok := r.meters.Load(name); ok {
		return m.(*Meter)
	}
	m, _ := r.meters.LoadOrStore(name, NewMeter())
	return m.(*Meter)
}

// FindIdle finds all meters that haven't been used since the given time.
func (r *MeterRegistry) FindIdle(since time.Time) []string {
	var idle []string
	r.walkIdle(since, func(key interface{}) {
		idle = append(idle, key.(string))
	})
	return idle
}

// TrimIdle trims that haven't been updated since the given time. Returns the
// number of timers trimmed.
func (r *MeterRegistry) TrimIdle(since time.Time) (trimmed int) {
	// keep these as interfaces to avoid allocating when calling delete.
	var idle []interface{}
	r.walkIdle(since, func(key interface{}) {
		idle = append(idle, since)
	})
	for _, i := range idle {
		r.meters.Delete(i)
	}
	return len(idle)
}

func (r *MeterRegistry) walkIdle(since time.Time, cb func(key interface{})) {
	// Yes, this is a global lock. However, all taking this does is pause
	// snapshotting.
	globalSweeper.snapshotMu.RLock()
	defer globalSweeper.snapshotMu.RUnlock()

	r.meters.Range(func(k, v interface{}) bool {
		// So, this _is_ slightly inaccurate.
		if v.(*Meter).snapshot.LastUpdate.Before(since) {
			cb(k)
		}
		return true
	})
}

// Remove removes the named meter from the registry.
//
// Note: The only reason to do this is to save a bit of memory. Unused meters
// don't consume any CPU (after they go idle).
func (r *MeterRegistry) Remove(name string) {
	r.meters.Delete(name)
}

// ForEach calls the passed function for each registered meter.
func (r *MeterRegistry) ForEach(iterFunc func(string, *Meter)) {
	r.meters.Range(func(k, v interface{}) bool {
		iterFunc(k.(string), v.(*Meter))
		return true
	})
}

// Clear removes all meters from the registry.
func (r *MeterRegistry) Clear() {
	r.meters.Range(func(k, v interface{}) bool {
		r.meters.Delete(k)
		return true
	})
}
