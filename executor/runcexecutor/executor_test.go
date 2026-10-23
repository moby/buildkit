//go:build linux

package runcexecutor

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	runc "github.com/containerd/go-runc"
	"github.com/moby/buildkit/executor"
	"github.com/stretchr/testify/require"
)

func TestProcKillerKillsProcessGroup(t *testing.T) {
	tests := []struct {
		name      string
		newKiller func(t *testing.T) procKiller
	}{
		{
			name: "run",
			newKiller: func(t *testing.T) procKiller {
				return newRunProcKiller(&runc.Runc{Command: "false"}, "test", filepath.Join(t.TempDir(), "pid"))
			},
		},
		{
			name: "exec",
			newKiller: func(t *testing.T) procKiller {
				killer, err := newExecProcKiller(&runc.Runc{Command: "false"}, "test")
				require.NoError(t, err)
				t.Cleanup(killer.Cleanup)
				return killer
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			killer := tt.newKiller(t)

			leader := exec.CommandContext(t.Context(), "sleep", "60")
			leader.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			require.NoError(t, leader.Start())

			member := exec.CommandContext(t.Context(), "sleep", "60")
			member.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: leader.Process.Pid}
			require.NoError(t, member.Start())

			require.NoError(t, os.WriteFile(killer.pidfile, []byte(strconv.Itoa(leader.Process.Pid)), 0o600))
			require.NoError(t, killer.Kill(t.Context()))

			for _, cmd := range []*exec.Cmd{leader, member} {
				done := make(chan error, 1)
				go func() {
					done <- cmd.Wait()
				}()
				select {
				case <-done:
				case <-time.After(10 * time.Second):
					t.Fatalf("process %d did not exit after kill", cmd.Process.Pid)
				}
				status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
				require.True(t, ok)
				require.Equal(t, syscall.SIGKILL, status.Signal())
			}
		})
	}
}

func TestProcKillerKillsMountNamespace(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("requires root")
	}

	tests := []struct {
		name                 string
		cloneflags           uintptr
		initExited           bool
		expectedErr          error
		expectedDetachedGone bool
	}{
		{
			name:                 "live init",
			cloneflags:           syscall.CLONE_NEWNS,
			expectedDetachedGone: true,
		},
		{
			name:                 "exited init",
			cloneflags:           syscall.CLONE_NEWNS,
			initExited:           true,
			expectedErr:          errors.New("did not terminate successfully"),
			expectedDetachedGone: true,
		},
		{
			name: "shared mount namespace",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := require.New(t)
			killer := newRunProcKiller(&runc.Runc{Command: "false"}, "test", filepath.Join(t.TempDir(), "pid"))
			t.Cleanup(killer.Cleanup)

			bystander := exec.CommandContext(t.Context(), "sleep", "60")
			bystander.SysProcAttr = &syscall.SysProcAttr{
				Setpgid:    true,
				Cloneflags: syscall.CLONE_NEWNS,
			}
			r.NoError(bystander.Start())

			detachedFile := filepath.Join(t.TempDir(), "detached")
			script := "setsid sh -c 'echo $$ > " + detachedFile + "; exec sleep 60' & exec sleep 60"
			leader := exec.CommandContext(t.Context(), "sh", "-c", script)
			leader.SysProcAttr = &syscall.SysProcAttr{
				Setpgid:    true,
				Cloneflags: tt.cloneflags,
			}
			r.NoError(leader.Start())
			leaderExited := make(chan struct{})
			go func() {
				defer close(leaderExited)
				_ = leader.Wait()
			}()

			r.Eventually(func() bool {
				data, err := os.ReadFile(detachedFile)
				return err == nil && strings.HasSuffix(string(data), "\n")
			}, 10*time.Second, time.Millisecond)
			data, err := os.ReadFile(detachedFile)
			r.NoError(err)
			detached, err := strconv.Atoi(strings.TrimSpace(string(data)))
			r.NoError(err)
			t.Cleanup(func() {
				_ = syscall.Kill(detached, syscall.SIGKILL)
			})

			r.NoError(os.WriteFile(killer.pidfile, []byte(strconv.Itoa(leader.Process.Pid)), 0o600))
			if tt.initExited {
				killer.pinFromPidfile(t.Context())
				r.NoError(leader.Process.Kill())
				<-leaderExited
			}

			err = killer.Kill(t.Context())
			if tt.expectedErr == nil {
				r.NoError(err)
			} else {
				r.ErrorContains(err, tt.expectedErr.Error())
			}
			select {
			case <-leaderExited:
			case <-time.After(10 * time.Second):
				t.Fatalf("process %d did not exit after kill", leader.Process.Pid)
			}
			status, ok := leader.ProcessState.Sys().(syscall.WaitStatus)
			r.True(ok)
			r.Equal(syscall.SIGKILL, status.Signal())
			if tt.expectedDetachedGone {
				r.Eventually(func() bool {
					return processGone(detached)
				}, 10*time.Second, 10*time.Millisecond)
			} else {
				r.Never(func() bool {
					return processGone(detached)
				}, 500*time.Millisecond, 10*time.Millisecond)
			}
			r.Never(func() bool {
				return processGone(bystander.Process.Pid)
			}, 500*time.Millisecond, 10*time.Millisecond)
		})
	}
}

func TestCleanupAfterSweep(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("requires root")
	}

	r := require.New(t)
	runC := &runc.Runc{
		Command: "sh",
		ExtraArgs: []string{
			"-c",
			"sleep 3",
			"fake-runc",
		},
	}
	killer := newRunProcKiller(runC, "test", filepath.Join(t.TempDir(), "pid"))

	bystander := exec.CommandContext(t.Context(), "sleep", "60")
	bystander.SysProcAttr = &syscall.SysProcAttr{
		Setpgid:    true,
		Cloneflags: syscall.CLONE_NEWNS,
	}
	r.NoError(bystander.Start())

	detachedFile := filepath.Join(t.TempDir(), "detached")
	script := "setsid sh -c 'echo $$ > " + detachedFile + "; exec sleep 60' & exec sleep 60"
	leader := exec.CommandContext(t.Context(), "sh", "-c", script)
	leader.SysProcAttr = &syscall.SysProcAttr{
		Setpgid:    true,
		Cloneflags: syscall.CLONE_NEWNS,
	}
	r.NoError(leader.Start())

	r.Eventually(func() bool {
		data, err := os.ReadFile(detachedFile)
		return err == nil && strings.HasSuffix(string(data), "\n")
	}, 10*time.Second, time.Millisecond)
	data, err := os.ReadFile(detachedFile)
	r.NoError(err)
	detached, err := strconv.Atoi(strings.TrimSpace(string(data)))
	r.NoError(err)
	t.Cleanup(func() {
		_ = syscall.Kill(detached, syscall.SIGKILL)
	})

	r.NoError(os.WriteFile(killer.pidfile, []byte(strconv.Itoa(leader.Process.Pid)), 0o600))
	ctx, cancel := context.WithCancelCause(t.Context())
	call := func(_ context.Context, started chan<- int, _ runc.IO, _ string) error {
		started <- leader.Process.Pid
		cancel(context.Canceled)
		_ = leader.Wait()
		return nil
	}
	w := &runcExecutor{}
	err = w.callWithIO(ctx, executor.ProcessInfo{}, nil, killer, call)
	r.NoError(err)
	killer.Cleanup()

	r.Eventually(func() bool {
		return processGone(detached)
	}, 10*time.Second, 10*time.Millisecond)
	r.Never(func() bool {
		return processGone(bystander.Process.Pid)
	}, 500*time.Millisecond, 10*time.Millisecond)
}

func processGone(pid int) bool {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return true
	}

	stat := string(data)
	fields := strings.Fields(stat[strings.LastIndexByte(stat, ')')+1:])
	return len(fields) > 0 && fields[0] == "Z"
}
