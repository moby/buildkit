package boltutil

import (
	"context"
	"sync"
)

// A drain blocks new entrants, but must be cancellable: an active transaction
// can depend on a nested transaction that is waiting to enter.
type gate struct {
	mu     sync.Mutex
	active int
	resume chan struct{}
	idle   chan struct{}
}

func (g *gate) enter() {
	for {
		g.mu.Lock()
		resume := g.resume
		if resume == nil {
			g.active++
			g.mu.Unlock()
			return
		}
		g.mu.Unlock()
		<-resume
	}
}

func (g *gate) exit() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.active--
	if g.active == 0 && g.idle != nil {
		close(g.idle)
		g.idle = nil
	}
}

// The caller serializes pauses and pairs a successful pause with unpause.
func (g *gate) pause(ctx context.Context) bool {
	g.mu.Lock()
	g.resume = make(chan struct{})
	idle := make(chan struct{})
	if g.active == 0 {
		close(idle)
	} else {
		g.idle = idle
	}
	g.mu.Unlock()
	select {
	case <-idle:
		if context.Cause(ctx) == nil {
			return true
		}
	case <-ctx.Done():
	}
	g.unpause()
	return false
}

func (g *gate) unpause() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.idle = nil
	close(g.resume)
	g.resume = nil
}
