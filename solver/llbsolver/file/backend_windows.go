package file

import (
	"context"

	"github.com/moby/buildkit/util/windows"
	"github.com/moby/buildkit/util/winprivileges"
	"github.com/moby/sys/user"
	copy "github.com/tonistiigi/fsutil/copy"
)

func mapUserToChowner(user *copy.User, _ *user.IdentityMapping) (copy.Chowner, error) {
	if user == nil || user.SID == "" {
		return func(old *copy.User) (*copy.User, error) {
			if old == nil || old.SID == "" {
				old = &copy.User{
					SID: windows.ContainerAdministratorSidString,
				}
			}
			return old, nil
		}, nil
	}
	return func(*copy.User) (*copy.User, error) {
		return user, nil
	}, nil
}

// copyWithElevatedPrivileges wraps copy.Copy to handle Windows protected system folders.
// On Windows, container snapshots mounted to the host filesystem include protected folders
// ("System Volume Information" and "WcSandboxState") at the mount root, which cause "Access is denied"
// errors when attempting to read their metadata. This function uses SeBackupPrivilege to allow
// reading these protected files.
//
// SeBackupPrivilege must be enabled process-wide (not thread-local) because copy.Copy spawns
// goroutines that may execute in different OS threads.
//
// Uses the centralized privilege manager to coordinate with other components and prevent
// race conditions in parallel builds.
func copyWithElevatedPrivileges(ctx context.Context, srcRoot string, src string, destRoot string, dest string, opt ...copy.Opt) error {
	if err := winprivileges.EnableSeBackupPrivilege(); err != nil {
		// Continue even if privilege elevation fails - it may already be enabled
		// or the process may not have permission to enable it
		_ = err
	}
	defer winprivileges.DisableSeBackupPrivilege()

	// Perform copy with elevated privileges
	return copy.Copy(ctx, srcRoot, src, destRoot, dest, opt...)
}
