//go:build linux

package runcexecutor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/containerd/go-runc"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"

	"github.com/moby/buildkit/executor"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/bklog"
)

func TestIsContainerNotRunning(t *testing.T) {
	t.Parallel()

	positive := []string{
		"container not running",
		"exit status 1: container not running",
		"runc kill: container not running",
		"Container Not Running",
		"container does not exist",
		`container "abc123" does not exist`,
		"no such process",
		"kill: no such process",
	}
	for _, msg := range positive {
		require.True(t, isContainerNotRunning(fmt.Errorf("%s", msg)), "expected match for %q", msg)
	}

	negative := []string{
		"permission denied",
		"signal: killed",
		"context deadline exceeded",
		"some other runc error",
	}
	for _, msg := range negative {
		require.False(t, isContainerNotRunning(fmt.Errorf("%s", msg)), "unexpected match for %q", msg)
	}
}

// newMockRuncWithError creates a fake runc executable that returns the given
// error message on stderr when called with "kill".
func newMockRuncWithError(t *testing.T, killErr string) *runc.Runc {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "mock_runc")
	script := fmt.Sprintf(`#!/bin/sh
if [ "$1" = "kill" ]; then
  echo "%s" >&2
  exit 1
fi
exit 0
`, killErr)
	err := os.WriteFile(path, []byte(script), 0700)
	require.NoError(t, err)
	return &runc.Runc{Command: path}
}

// newMockRunc creates a fake runc executable that returns "container not running"
// on kill, simulating the standard runc error condition.
func newMockRunc(t *testing.T) *runc.Runc {
	t.Helper()
	return newMockRuncWithError(t, "container not running")
}

// TestZombieRuncDeadlock validates the fix for the zombified runc process deadlock.
// This is the definitive test that simulates the real-world conditions.
func TestZombieRuncDeadlock(t *testing.T) {
	t.Parallel()

	// Setup a mock runc that will return the "container not running" error on Kill.
	mockRunc := newMockRunc(t)

	// Setup the executor and a cancellable parent context.
	w := &runcExecutor{runc: mockRunc}
	parentCtx, cancelParent := context.WithCancel(context.Background())
	defer cancelParent()

	// This mock `call` function simulates a hanging `runc run` process.
	// It spawns a real subprocess and waits for it, exactly like the real code.
	call := func(ctx context.Context, started chan<- int, io runc.IO, pidfile string) error {
		// Spawn a long-running subprocess to simulate a hanging runc.
		cmd := exec.CommandContext(ctx, "sleep", "30s")
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

		if err := cmd.Start(); err != nil {
			return errors.Wrap(err, "failed to start sleep subprocess in mock call")
		}

		if started != nil {
			started <- cmd.Process.Pid
			close(started)
		}

		// This is the call that hangs in the real deadlock,
		// and it will only unblock when the 'sleep' process is killed or the call context is canceled.
		return cmd.Wait()
	}

	killer := newRunProcKiller(mockRunc, "test-id")
	process := executor.ProcessInfo{Meta: executor.Meta{NetMode: pb.NetMode_NONE}}

	// Execute callWithIO in a goroutine and orchestrate the deadlock.
	done := make(chan error, 1)
	go func() {
		done <- w.callWithIO(parentCtx, process, nil, killer, call)
	}()

	// Give the goroutine time to get into the blocking `cmd.Wait()` call.
	time.Sleep(100 * time.Millisecond)

	bklog.G(parentCtx).Debug("test: canceling parent context to trigger cleanup")
	cancelParent()

	select {
	case err := <-done:
		// We expect a "signal: killed" error because our fix kills the sleep process,
		// which unblocks cmd.Wait() with an error.
		require.Error(t, err)
		require.Contains(t, err.Error(), "signal: killed")
		bklog.G(parentCtx).Debugf("test: successfully received error after breaking deadlock: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("callWithIO deadlocked")
	}
}
