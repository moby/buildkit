package yamux

import "time"

type ping struct {
	id uint32
	// written to by the session on ping response
	pingResponse chan struct{}

	// closed by the Ping call that sent the ping when done.
	done chan struct{}
	// result set before done is closed.
	err      error
	duration time.Duration
}

func newPing(id uint32) *ping {
	return &ping{
		id:           id,
		pingResponse: make(chan struct{}, 1),
		done:         make(chan struct{}),
	}
}

func (p *ping) finish(val time.Duration, err error) {
	p.err = err
	p.duration = val
	close(p.done)
}

func (p *ping) wait() (time.Duration, error) {
	<-p.done
	return p.duration, p.err
}
