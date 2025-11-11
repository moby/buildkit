//go:build windows

package filesync

import (
	"github.com/moby/buildkit/util/winprivileges"
	"github.com/pkg/errors"
	"github.com/tonistiigi/fsutil"
)

func sendDiffCopy(stream Stream, fs fsutil.FS, progress progressCb) error {
	// Enable SeBackupPrivilege to allow copying special Windows metadata files.
	// Uses the centralized privilege manager to prevent race conditions when
	// multiple goroutines in fsutil.Send need the privilege concurrently.
	if err := winprivileges.EnableSeBackupPrivilege(); err != nil {
		// Continue even if privilege elevation fails - it may already be enabled
		// or the process may not have permission to enable it
		_ = err
	}
	defer winprivileges.DisableSeBackupPrivilege()
	return errors.WithStack(fsutil.Send(stream.Context(), stream, fs, progress))
}
