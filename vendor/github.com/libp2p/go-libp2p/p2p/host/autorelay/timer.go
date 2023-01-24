package autorelay

import (
	"time"

	"github.com/benbjohnson/clock"
)

type timer struct {
	timer   *clock.Timer
	running bool
	read    bool
}

func newTimer(cl clock.Clock) *timer {
	t := cl.Timer(100 * time.Hour) // There's no way to initialize a stopped timer
	t.Stop()
	return &timer{timer: t}
}

func (t *timer) Chan() <-chan time.Time {
	return t.timer.C
}

func (t *timer) Stop() {
	if !t.running {
		return
	}
	if !t.timer.Stop() && !t.read {
		<-t.timer.C
	}
	t.read = false
}

func (t *timer) SetRead() {
	t.read = true
}

func (t *timer) Reset(d time.Duration) {
	t.Stop()
	t.timer.Reset(d)
}
