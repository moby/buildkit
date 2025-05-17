package contenthash

import (
	"context"
	"path/filepath"

	"github.com/Microsoft/go-winio"
)

var privileges = []string{winio.SeBackupPrivilege}

func (cc *cacheContext) walk(scanPath string, walkFunc filepath.WalkFunc) error {
	// elevating the admin privileges to walk special files/directory
	// like `System Volume Information`, etc. See similar in #4994
	return winio.RunWithPrivileges(privileges, func() error {
		return filepath.Walk(scanPath, walkFunc)
	})
}

// Adds the SeBackupPrivilege to the process
// to be able to access some special files and directories.
// Then later disables the privilege after the context is cancelled.
func enableProcessPrivileges(ctx context.Context) {
	_ = winio.EnableProcessPrivileges(privileges)

	// spin off a goroutine that will wait
	// until the context is cancelled, before calling
	// winio.DisableProcessPrivileges.
	// Previously, a defer call to this would sometimes
	// be called earlier before all the inner goroutines
	// are done, and cause failures. See #5906
	go func() {
		<-ctx.Done()
		_ = winio.DisableProcessPrivileges(privileges)
	}()
}
