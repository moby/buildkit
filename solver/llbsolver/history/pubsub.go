package history

import "sync"

type pubsub[T any] struct {
	mu sync.Mutex
	m  map[*channel[T]]struct{}
}

func (p *pubsub[T]) Subscribe() *channel[T] {
	p.mu.Lock()
	c := &channel[T]{
		ps:   p,
		ch:   make(chan T, 32),
		done: make(chan struct{}),
	}
	p.m[c] = struct{}{}
	p.mu.Unlock()
	return c
}

func (p *pubsub[T]) Send(v T) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for c := range p.m {
		if len(c.pending) == 0 {
			select {
			case c.ch <- v:
				continue
			default:
			}
			go c.drain(v)
		}
		c.pending = append(c.pending, v)
	}
}

func (p *pubsub[T]) Close() {
	p.mu.Lock()
	channels := make([]*channel[T], 0, len(p.m))
	for c := range p.m {
		channels = append(channels, c)
	}
	p.mu.Unlock()
	for _, c := range channels {
		c.close()
	}
}

type channel[T any] struct {
	ps        *pubsub[T]
	ch        chan T
	done      chan struct{}
	closeOnce sync.Once
	pending   []T // protected by ps.mu; only the first send starts a drainer
}

func (p *channel[T]) drain(v T) {
	for {
		select {
		case p.ch <- v:
		case <-p.done:
			return
		}
		p.ps.mu.Lock()
		clear(p.pending[:1])
		p.pending = p.pending[1:]
		if len(p.pending) == 0 {
			p.pending = nil
			p.ps.mu.Unlock()
			return
		}
		v = p.pending[0]
		p.ps.mu.Unlock()
	}
}

func (p *channel[T]) close() {
	p.closeOnce.Do(func() {
		p.ps.mu.Lock()
		delete(p.ps.m, p)
		p.ps.mu.Unlock()
		close(p.done)
	})
}
