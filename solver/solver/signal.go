package solver

import "sync"

func newSignaller() *signal {
	return &signal{}
}

type signal struct {
	mu sync.Mutex
	ch chan struct{}
}

func (s *signal) Wait() chan struct{} {
	s.mu.Lock()
	if s.ch == nil {
		s.ch = make(chan struct{})
	}
	ch := s.ch
	s.mu.Unlock()
	return ch
}

func (s *signal) Reset() chan struct{} {
	s.mu.Lock()
	ch := s.ch
	select {
	case <-ch:
		ch = make(chan struct{})
		s.ch = ch
	default:
	}
	s.mu.Unlock()
	return ch
}

func (s *signal) Signal() {
	s.mu.Lock()
	if s.ch == nil {
		s.ch = make(chan struct{})
	}
	ch := s.ch
	select {
	case <-ch:
	default:
		close(ch)
	}
	s.mu.Unlock()
}
