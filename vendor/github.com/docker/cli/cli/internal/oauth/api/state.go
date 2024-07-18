package api

import (
	"time"
)

// State represents the state of exchange after submitting.
type State struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri_complete"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

// IntervalDuration returns the duration that should be waited between each auth
// polling event.
func (s State) IntervalDuration() time.Duration {
	return time.Second * time.Duration(s.Interval)
}
