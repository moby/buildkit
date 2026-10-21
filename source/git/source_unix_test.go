//go:build !windows

package git

import (
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func init() {
	// Reset umask to zero to match buildkitd and to make sure the tests do not rely on standard umask from the host.
	syscall.Umask(0)
}

func assertSymlinkMtime(t *testing.T, path string, target time.Time) {
	t.Helper()

	fi, err := os.Lstat(path)
	require.NoError(t, err)
	require.Equal(t, target, fi.ModTime().UTC())
}
