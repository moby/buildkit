//go:build windows

package winprivileges

import (
	"sync"

	"github.com/Microsoft/go-winio"
)

var (
	privileges        = []string{winio.SeBackupPrivilege}
	privilegeLock     sync.Mutex
	privilegeRefCount int
)

// EnableSeBackupPrivilege adds the SeBackupPrivilege to the process.
// It uses reference counting to safely handle parallel operations.
// Must be paired with DisableSeBackupPrivilege.
func EnableSeBackupPrivilege() error {
	privilegeLock.Lock()
	defer privilegeLock.Unlock()

	if privilegeRefCount == 0 {
		if err := winio.EnableProcessPrivileges(privileges); err != nil {
			return err
		}
	}
	privilegeRefCount++
	return nil
}

// DisableSeBackupPrivilege removes the SeBackupPrivilege from the process.
// It uses reference counting, only actually disabling when the count reaches zero.
func DisableSeBackupPrivilege() {
	privilegeLock.Lock()
	defer privilegeLock.Unlock()

	if privilegeRefCount > 0 {
		privilegeRefCount--
		if privilegeRefCount == 0 {
			_ = winio.DisableProcessPrivileges(privileges)
		}
	}
}

// RunWithPrivilege executes the given function with SeBackupPrivilege enabled.
// This is a convenience wrapper that handles enable/disable automatically.
func RunWithPrivilege(fn func() error) error {
	if err := EnableSeBackupPrivilege(); err != nil {
		return err
	}
	defer DisableSeBackupPrivilege()
	return fn()
}
