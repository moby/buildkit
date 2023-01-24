// +build !linux

package watchdog

import (
	"fmt"
	"time"
)

// CgroupDriven is only available in Linux. This method will error.
func CgroupDriven(frequency time.Duration, policyCtor PolicyCtor) (err error, stopFn func()) {
	return fmt.Errorf("cgroups-driven watchdog: %w", ErrNotSupported), nil
}
