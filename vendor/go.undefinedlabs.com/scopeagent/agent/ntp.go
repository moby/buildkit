package agent

import (
	"github.com/beevik/ntp"
	"sync"
	"time"
)

const (
	server  = "pool.ntp.org"
	retries = 5
	timeout = 1 * time.Second
	backoff = 1 * time.Second
)

var (
	ntpOffset time.Duration
	once      sync.Once
)

// Gets the NTP offset from the ntp server pool
func getNTPOffset() (time.Duration, error) {
	var ntpError error = nil
	for i := 1; i <= retries; i++ {
		r, err := ntp.QueryWithOptions(server, ntp.QueryOptions{Timeout: timeout})
		if err == nil {
			return r.ClockOffset, nil
		}
		ntpError = err
		time.Sleep(backoff)
	}
	return 0, ntpError
}

// Applies the NTP offset to the given time
func (r *SpanRecorder) applyNTPOffset(t time.Time) time.Time {
	once.Do(func() {
		if r.debugMode {
			r.logger.Println("calculating ntp offset.")
		}
		offset, err := getNTPOffset()
		if err == nil {
			ntpOffset = offset
			r.logger.Printf("ntp offset: %v\n", ntpOffset)
		} else {
			r.logger.Printf("error calculating the ntp offset: %v\n", err)
		}
	})
	return t.Add(ntpOffset)
}
