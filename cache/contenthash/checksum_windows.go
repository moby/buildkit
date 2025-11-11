package contenthash

import (
	"path/filepath"

	"github.com/Microsoft/go-winio"
	"github.com/moby/buildkit/util/winprivileges"
)

func (cc *cacheContext) walk(scanPath string, walkFunc filepath.WalkFunc) error {
	// elevating the admin privileges to walk special files/directory
	// like `System Volume Information`, etc. See similar in #4994
	return winio.RunWithPrivileges([]string{winio.SeBackupPrivilege}, func() error {
		return filepath.Walk(scanPath, walkFunc)
	})
}

// Adds the SeBackupPrivilege to the process
// to be able to access some special files and directories.
// Uses the centralized privilege manager to coordinate with other components.
func enableProcessPrivileges() {
	_ = winprivileges.EnableSeBackupPrivilege()
}

// Disables the SeBackupPrivilege on the process
// once the group of functions that needed it is complete.
// Uses the centralized privilege manager to coordinate with other components.
func disableProcessPrivileges() {
	winprivileges.DisableSeBackupPrivilege()
}
