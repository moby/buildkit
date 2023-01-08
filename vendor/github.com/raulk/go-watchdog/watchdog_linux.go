package watchdog

import (
	"fmt"
	"os"
	"time"

	"github.com/containerd/cgroups"
)

var (
	pid          = os.Getpid()
	memSubsystem = cgroups.SingleSubsystem(cgroups.V1, cgroups.Memory)
)

// CgroupDriven initializes a cgroups-driven watchdog. It will try to discover
// the memory limit from the cgroup of the process (derived from /proc/self/cgroup),
// or from the root cgroup path if the PID == 1 (which indicates that the process
// is running in a container).
//
// Memory usage is calculated by querying the cgroup stats.
//
// This function will return an error immediately if the OS does not support cgroups,
// or if another error occurs during initialization. The caller can then safely fall
// back to the system driven watchdog.
func CgroupDriven(frequency time.Duration, policyCtor PolicyCtor) (err error, stopFn func()) {
	// use self path unless our PID is 1, in which case we're running inside
	// a container and our limits are in the root path.
	path := cgroups.NestedPath("")
	if pid := os.Getpid(); pid == 1 {
		path = cgroups.RootPath
	}

	cgroup, err := cgroups.Load(memSubsystem, path)
	if err != nil {
		return fmt.Errorf("failed to load cgroup for process: %w", err), nil
	}

	var limit uint64
	if stat, err := cgroup.Stat(); err != nil {
		return fmt.Errorf("failed to load memory cgroup stats: %w", err), nil
	} else if stat.Memory == nil || stat.Memory.Usage == nil {
		return fmt.Errorf("cgroup memory stats are nil; aborting"), nil
	} else {
		limit = stat.Memory.Usage.Limit
	}

	if limit == 0 {
		return fmt.Errorf("cgroup limit is 0; refusing to start memory watchdog"), nil
	}

	policy, err := policyCtor(limit)
	if err != nil {
		return fmt.Errorf("failed to construct policy with limit %d: %w", limit, err), nil
	}

	if err := start(UtilizationProcess); err != nil {
		return err, nil
	}

	_watchdog.wg.Add(1)
	go pollingWatchdog(policy, frequency, limit, func() (uint64, error) {
		stat, err := cgroup.Stat()
		if err != nil {
			return 0, err
		} else if stat.Memory == nil || stat.Memory.Usage == nil {
			return 0, fmt.Errorf("cgroup memory stats are nil; aborting")
		}
		return stat.Memory.Usage.Usage, nil
	})

	return nil, stop
}
