//go:build cgo && !nowatchdog

package connmgr

import "github.com/raulk/go-watchdog"

func registerWatchdog(cb func()) (unregister func()) {
	return watchdog.RegisterPostGCNotifee(cb)
}

// WithEmergencyTrim is an option to enable trimming connections on memory emergency.
func WithEmergencyTrim(enable bool) Option {
	return func(cfg *config) error {
		cfg.emergencyTrim = enable
		return nil
	}
}
