package sshutil

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/moby/buildkit/util/testutil/sshutil/probe"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

func TestAgentConnections(t *testing.T) {
	a := NewAgent(t)
	for i := 1; i <= 3; i++ {
		c, err := probe.Dial(t.Context(), a.Endpoint)
		require.NoError(t, err)
		require.NoError(t, c.SetDeadline(time.Now().Add(5*time.Second)))
		keys, err := agent.NewClient(c).List()
		require.NoError(t, err)
		require.NoError(t, probe.CheckIdentity(keys, a.PublicKey))
		accepted, active, closed := a.Counts()
		require.Equal(t, i, accepted)
		require.Equal(t, 1, active)
		require.Equal(t, i-1, closed)
		require.NoError(t, c.Close())
		a.WaitIdle(t, i)
	}
	a.CheckKey(t)
}

func TestAgentCleanup(t *testing.T) {
	var a *Agent
	cleanup := t.Cleanup
	t.Run("open-connection", func(t *testing.T) {
		a = NewAgent(t)
		c, err := probe.Dial(t.Context(), a.Endpoint)
		require.NoError(t, err)
		cleanup(func() { _ = c.Close() })
		require.NoError(t, c.SetDeadline(time.Now().Add(5*time.Second)))
		_, err = agent.NewClient(c).List()
		require.NoError(t, err)
		// Registered after the fixture: leave the connection open until its cleanup.
		t.Cleanup(func() { _, active, _ := a.Counts(); require.Equal(t, 1, active) })
	})
	_, active, _ := a.Counts()
	require.Zero(t, active)
}

func TestProbeAgent(t *testing.T) {
	a := NewAgent(t)
	readOnly := Serve(t, readOnlyAgent{a.Keyring})
	readOnly.RequireSequentialConnections()
	r, err := probe.Run(t.Context(), probe.Config{
		Endpoint: readOnly.Endpoint, Expected: a.PublicKey, Cycles: 4, Mutate: true,
	})
	require.NoError(t, err)
	require.Equal(t, []string{a.PublicKey}, r.Keys)
	require.Equal(t, 4, r.Connections)
	require.True(t, r.AddRejected)
	require.True(t, r.RemoveAllRejected)
	readOnly.WaitIdle(t, 4)
	a.CheckKey(t)

	t.Run("empty", func(t *testing.T) {
		empty := Serve(t, agent.NewKeyring())
		_, err := probe.Run(t.Context(), probe.Config{Endpoint: empty.Endpoint, Expected: a.PublicKey, Cycles: 1})
		require.ErrorContains(t, err, "expected exactly public key")
		empty.WaitIdle(t, 1)
	})
	t.Run("wrong", func(t *testing.T) {
		other := NewAgent(t)
		_, err := probe.Run(t.Context(), probe.Config{Endpoint: other.Endpoint, Expected: a.PublicKey, Cycles: 1})
		require.ErrorContains(t, err, "expected exactly public key")
		other.WaitIdle(t, 1)
	})
	t.Run("present-is-not-absent", func(t *testing.T) {
		_, err := probe.Run(t.Context(), probe.Config{Endpoint: a.Endpoint, Absent: true})
		require.ErrorContains(t, err, "unexpectedly exists")
		a.WaitIdle(t, 1)
	})
}

func TestBuildProbe(t *testing.T) {
	a := NewAgent(t)
	path := filepath.Join(WorkDir(t), "sshprobe.exe")
	require.NoError(t, os.WriteFile(path, BuildProbe(t, runtime.GOARCH), 0600))
	cmd := exec.CommandContext(t.Context(), path, "-endpoint", a.Endpoint, "-defaults=false", "-expected", a.PublicKey, "-cycles", "2")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	var report probe.Report
	require.NoError(t, json.Unmarshal(out, &report))
	require.Equal(t, []string{a.PublicKey}, report.Keys)
	require.Equal(t, 2, report.Connections)
	a.WaitIdle(t, 2)
}

func TestSequentialConnectionsRequireClose(t *testing.T) {
	first, second := net.Pipe()
	defer first.Close()
	defer second.Close()
	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(errors.New("test: previous connection is still open"))
	a := &Agent{
		ctx: ctx, conns: map[net.Conn]struct{}{first: {}, second: {}},
		changed: make(chan struct{}),
	}
	require.ErrorContains(t, a.waitPrevious(), "previous connection is still open")
	require.Zero(t, a.checks)
	require.Len(t, a.errs, 1)
	delete(a.conns, first)
	require.NoError(t, a.waitPrevious())
	require.Equal(t, 1, a.checks)
}

func TestProbeAbsent(t *testing.T) {
	a := NewAgent(t)
	endpoint := a.Endpoint + "-missing"
	r, err := probe.Run(t.Context(), probe.Config{Endpoint: endpoint, Absent: true})
	require.NoError(t, err)
	require.True(t, r.Absent)
	require.Zero(t, r.Connections)
}

type readOnlyAgent struct{ agent.Agent }

func (readOnlyAgent) Add(agent.AddedKey) error   { return errors.New("read only") }
func (readOnlyAgent) Remove(ssh.PublicKey) error { return errors.New("read only") }
func (readOnlyAgent) RemoveAll() error           { return errors.New("read only") }
