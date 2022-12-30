package gonso

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Pool manages a pool of Sets.
// It is safe to use a Pool from multiple goroutines.
// Create one using `NewPool` with the flags you want to use for sets managed by this pool.
type Pool struct {
	mu    sync.Mutex
	cvar  *sync.Cond
	sets  []Set
	flags int

	afterCreate func() error

	// notify is used for testing purposes
	// it is called when a set is created by `Run`
	notify func()
}

// NewPool creates a new pool with the given flags.
// Call `pool.Run` start filling the pool.
//
// `afterCreate` is called after a set is created for the pool.
// This is useful to set up the set before it is needed.
func NewPool(flags int, afterCreate func() error) *Pool {
	p := &Pool{
		flags: flags,
	}
	p.cvar = sync.NewCond(&p.mu)
	return p
}

func (p *Pool) runAfterCreate(s Set) error {
	if p.afterCreate == nil {
		return nil
	}
	var err error
	doErr := s.Do(func() {
		err = p.afterCreate()
	})
	if doErr != nil {
		s.Close()
		return doErr
	}
	if err != nil {
		s.Close()
		err = fmt.Errorf("error running after create function: %w", err)
	}
	return err
}

func (p *Pool) get() (Set, error) {
	s, err := Unshare(p.flags)
	if err != nil {
		return Set{}, err
	}
	if err := p.runAfterCreate(s); err != nil {
		return Set{}, err
	}
	return s, nil
}

// Get returns a set from the pool.
// If there are no sets available, Get will create a new one.
func (p *Pool) Get() (Set, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if len(p.sets) == 0 {
		return p.get()
	}

	s := p.sets[0]
	p.sets = p.sets[1:]
	p.cvar.Signal()

	return s, nil
}

// Put returns a set to the pool.  It is up to the caller to ensure the set is
// in a re-usable state.  For instance if the set was created with CLONE_NEWNET
// and there are changes to the network namespace, the caller is responsible for
// resetting that namespace.
//
// In most cases it is probably best to never call `Put` and instead close the set and throw it away.
func (p *Pool) Put(s Set) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.sets = append(p.sets, s)
}

// Len shows how many sets are currently in the pool.
func (p *Pool) Len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.sets)
}

// Run makes sure that the pool has at least `n` sets available.
// Run spins up a new goroutine to maintain the pool.
// The goroutine will exit when the context is cancelled.
//
// The returned context will have an error set if the pool fails to create a set or is otherwise cancelled.
func (p *Pool) Run(ctx context.Context, n int) (_ context.Context, cancel func()) {
	ctx, cancelP := newPoolContext(ctx)
	go func() {
		cancelP(p.run(ctx, n))
	}()
	return ctx, func() {
		cancelP(nil)
	}
}

func (p *Pool) run(ctx context.Context, n int) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.sets == nil {
		p.sets = make([]Set, 0, n)
	}

	defer func() {
		for _, s := range p.sets {
			s.Close()
		}
		p.sets = nil
		if p.notify != nil {
			p.notify()
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		for len(p.sets) >= n {
			p.cvar.Wait()
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		s, err := p.get()
		if err != nil {
			return err
		}
		p.sets = append(p.sets, s)
		if p.notify != nil {
			p.notify()
		}
	}
}

func newPoolContext(ctx context.Context) (context.Context, func(error)) {
	ctx, cancel := context.WithCancel(ctx)
	p := &poolContext{
		ctx: ctx,
	}
	return p, func(err error) {
		p.mu.Lock()
		if err == nil {
			err = context.Canceled
		}
		if p.err == nil {
			p.err = err
		}
		p.mu.Unlock()
		cancel()
	}
}

type poolContext struct {
	ctx context.Context
	mu  sync.Mutex
	err error
}

func (p *poolContext) Deadline() (deadline time.Time, ok bool) {
	return p.ctx.Deadline()
}

func (p *poolContext) Done() <-chan struct{} {
	return p.ctx.Done()
}

func (p *poolContext) Err() error {
	p.mu.Lock()
	err := p.err
	p.mu.Unlock()

	if err != nil {
		return err
	}

	return p.ctx.Err()
}

func (p *poolContext) Value(key interface{}) interface{} {
	return p.ctx.Value(key)
}
