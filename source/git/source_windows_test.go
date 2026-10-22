package git

import (
	"testing"
	"time"
)

// lchtimes is a no-op on Windows, so symlink timestamps are not checked.
func assertSymlinkMtime(_ *testing.T, _ string, _ time.Time) {}
