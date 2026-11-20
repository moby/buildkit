package integration

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/moby/buildkit/identity"
	"github.com/stretchr/testify/require"
)

func TestWaitSocketProcessExit(t *testing.T) {
	if os.Getenv("BUILDKIT_SOCKET_EXIT_HELPER") == "1" {
		_, _ = io.Copy(io.Discard, os.Stdin)
		return
	}

	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestWaitSocketProcessExit$") //nolint:gosec // Re-execute this test binary as the child process.
	cmd.Env = append(os.Environ(), "BUILDKIT_SOCKET_EXIT_HELPER=1")
	cmd.Stderr = io.Discard
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	stop, stopped, err := StartCmd(cmd, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, stop()) })

	// Let the child exit while WaitSocket polls a socket it never opens.
	require.NoError(t, stdin.Close())
	err = WaitSocket(missingSocket(t), 10*time.Second, cmd, stopped)
	require.ErrorContains(t, err, "process exited while waiting for socket")
	require.ErrorContains(t, err, cmd.String())
}

func TestWaitSocketWithoutProcess(t *testing.T) {
	err := WaitSocket(missingSocket(t), 0, nil, nil)
	require.ErrorContains(t, err, "failed dialing socket")
}

func missingSocket(t *testing.T) string {
	t.Helper()
	return UnixOrWindows(filepath.Join(t.TempDir(), "missing.sock"), `\\.\pipe\buildkit-waitsocket-`+identity.NewID())
}
