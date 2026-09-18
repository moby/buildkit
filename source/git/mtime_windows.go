//go:build windows

package git

import "time"

func lchtimes(string, time.Time) error {
	return nil
}
