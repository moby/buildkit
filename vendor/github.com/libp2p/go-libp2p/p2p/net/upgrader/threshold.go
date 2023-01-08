package upgrader

import (
	"sync"
)

func newThreshold(cutoff int) *threshold {
	t := &threshold{
		threshold: cutoff,
	}
	t.cond.L = &t.mu
	return t
}

type threshold struct {
	mu   sync.Mutex
	cond sync.Cond

	count     int
	threshold int
}

// Acquire increments the counter. It will not block.
func (t *threshold) Acquire() {
	t.mu.Lock()
	t.count++
	t.mu.Unlock()
}

// Release decrements the counter.
func (t *threshold) Release() {
	t.mu.Lock()
	if t.count == 0 {
		panic("negative count")
	}
	if t.threshold == t.count {
		t.cond.Broadcast()
	}
	t.count--
	t.mu.Unlock()
}

// Wait waits for the counter to drop below the threshold
func (t *threshold) Wait() {
	t.mu.Lock()
	for t.count >= t.threshold {
		t.cond.Wait()
	}
	t.mu.Unlock()
}
