package bgfunc

import (
	"sync"

	"golang.org/x/net/context"
)

func New(ctx context.Context, fn func(context.Context, func()) error) (*F, error) {
	f := &F{mainCtx: ctx, f: fn}
	f.cond = sync.NewCond(f.mu.RLocker())
	return f, nil
}

type F struct {
	mainCtx context.Context
	mu      sync.RWMutex
	cond    *sync.Cond
	running bool
	runMu   sync.Mutex
	f       func(context.Context, func()) error

	done      bool
	err       error
	cancelCtx func()
	ctxErr    chan error // this channel is used for the caller to wait cancellation error

	semMu sync.Mutex
	sem   int
}

func (f *F) NewCaller() *Caller {
	c := &Caller{F: f, active: true}
	f.semMu.Lock()
	f.addSem()
	f.semMu.Unlock()
	return c
}

func (f *F) run() {
	f.runMu.Lock()
	if !f.running && !f.done {
		f.running = true
		ctx, cancel := context.WithCancel(f.mainCtx)
		ctxErr := make(chan error, 1)
		f.cancelCtx = cancel
		f.ctxErr = ctxErr
		go func() {
			var err error
			var nodone bool
			defer func() {
				// release all cancellations
				f.runMu.Lock()
				f.running = false
				f.runMu.Unlock()
				f.mu.Lock()
				if !nodone {
					f.done = true
					f.err = err
				}
				f.cond.Broadcast()
				ctxErr <- err
				f.mu.Unlock()
			}()
			err = f.f(ctx, func() {
				f.cond.Broadcast()
			})
			select {
			case <-ctx.Done():
				nodone = true // don't set f.done
			default:
			}
		}()
	}
	f.runMu.Unlock()
}

func (f *F) addSem() {
	f.sem++
}

func (f *F) clearSem() error {
	f.sem--
	var err error
	if cctx := f.cancelCtx; f.sem == 0 && cctx != nil {
		cctx()
		err = <-f.ctxErr
		f.cancelCtx = nil
	}
	return err
}

type Caller struct {
	*F
	active bool
}

func (c *Caller) Call(ctx context.Context, f func() (interface{}, error)) (interface{}, error) {
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			c.F.mu.Lock()
			c.F.cond.Broadcast()
			c.F.mu.Unlock()
		case <-done:
		}
	}()

	c.F.mu.RLock()
	for {
		select {
		case <-ctx.Done():
			c.F.mu.RUnlock()
			if err := c.Cancel(); err != nil {
				return nil, err
			}
			return nil, ctx.Err()
		default:
		}

		if err := c.F.err; err != nil {
			c.F.mu.RUnlock()
			return nil, err
		}

		c.ensureStarted()

		v, err := f()
		if err != nil {
			c.F.mu.RUnlock()
			return nil, err
		}

		if v != nil {
			c.F.mu.RUnlock()
			return v, nil
		}

		if c.F.done {
			c.F.mu.RUnlock()
			return nil, nil
		}

		c.F.cond.Wait()
	}
}

func (c *Caller) Cancel() error {
	c.F.semMu.Lock()
	defer c.F.semMu.Unlock()
	if c.active {
		c.active = false
		return c.F.clearSem()
	}
	return nil
}

// called with readlock
func (c *Caller) ensureStarted() {
	if c.F.done {
		return
	}
	c.F.semMu.Lock()
	defer c.F.semMu.Unlock()

	if !c.active {
		c.active = true
		c.F.addSem()
	}

	c.F.run()
}
