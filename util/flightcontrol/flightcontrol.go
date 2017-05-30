package flightcontrol

import (
	"runtime"
	"sync"
	"time"

	"github.com/pkg/errors"

	"golang.org/x/net/context"
)

// flightcontrol is like singleflight but with support for cancellation and
// nested progress reporting

var errRetry = errors.Errorf("retry")

type Group struct {
	mu sync.Mutex       // protects m
	m  map[string]*call // lazily initialized
}

func (g *Group) Do(ctx context.Context, key string, fn func(ctx context.Context) (interface{}, error)) (v interface{}, err error, shared bool) {
	g.mu.Lock()
	if g.m == nil {
		g.m = make(map[string]*call)
	}
	if c, ok := g.m[key]; ok { // register 2nd waiter
		g.mu.Unlock()
		v, err, shared := c.wait(ctx)
		if err == errRetry {
			runtime.Gosched()
			return g.Do(ctx, key, fn)
		}
		return v, err, shared
	}
	c := &call{fn: fn, ready: make(chan struct{})}
	g.m[key] = c
	go func() {
		// cleanup after a caller has returned
		<-c.ready
		g.mu.Lock()
		delete(g.m, key)
		g.mu.Unlock()
	}()
	g.mu.Unlock()
	return c.wait(ctx)
}

type call struct {
	mu     sync.Mutex
	result interface{}
	err    error
	ready  chan struct{}
	ctx    *ctx
	fn     func(ctx context.Context) (interface{}, error)
}

func (c *call) wait(ctx context.Context) (v interface{}, err error, shared bool) {
	c.mu.Lock()
	// detect case where caller has just returned, let it clean up before
	select {
	case <-c.ready:
		c.mu.Unlock()
		return nil, errRetry, false
	default:
	}
	if c.ctx == nil { // first invocation, register shared context
		c.ctx = newContext()
		c.ctx.append(ctx)
		go func() {
			v, err := c.fn(c.ctx)
			c.mu.Lock()
			c.result = v
			c.err = err
			c.mu.Unlock()
			close(c.ready)
		}()
	} else {
		c.ctx.append(ctx)
	}
	c.mu.Unlock()
	select {
	case <-ctx.Done():
		select {
		case <-c.ctx.Done():
			// if this cancelled the last context, then wait for function to shut down
			// and don't accept any more callers
			<-c.ready
			return c.result, c.err, false
		default:
			return nil, ctx.Err(), false
		}
	case <-c.ready:
		return c.result, c.err, false // shared not implemented yet
	}
}

type ctx struct {
	mu   sync.Mutex
	ctxs []context.Context
	done chan struct{}
	err  error
}

func newContext() *ctx {
	return &ctx{done: make(chan struct{})}
}

func (c *ctx) append(ctx context.Context) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ctxs = append(c.ctxs, ctx)
	go func() {
		select {
		case <-c.done:
		case <-ctx.Done():
			c.mu.Lock()
			c.signalDone()
			c.mu.Unlock()
		}
	}()
}

// call with lock
func (c *ctx) signalDone() {
	select {
	case <-c.done:
	default:
		var err error
		for _, ctx := range c.ctxs {
			select {
			case <-ctx.Done():
				err = ctx.Err()
			default:
				return
			}
		}
		c.err = err
		close(c.done)
	}
}

func (c *ctx) Deadline() (deadline time.Time, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, ctx := range c.ctxs {
		select {
		case <-ctx.Done():
		default:
			dl, ok := ctx.Deadline()
			if ok {
				return dl, ok
			}
		}
	}
	return time.Time{}, false
}

func (c *ctx) Done() <-chan struct{} {
	c.mu.Lock()
	c.signalDone()
	c.mu.Unlock()
	return c.done
}

func (c *ctx) Err() error {
	select {
	case <-c.Done():
		return c.err
	default:
		return nil
	}
}

func (c *ctx) Value(key interface{}) interface{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, ctx := range c.ctxs {
		select {
		case <-ctx.Done():
		default:
			return ctx.Value(key)
		}
	}
	return nil
}
