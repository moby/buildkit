// Package sshutil provides isolated SSH agents and probes for Windows integration tests.
package sshutil

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

// WorkDir keeps fixture artifacts outside the build context and removes them at cleanup.
func WorkDir(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

type Agent struct {
	Endpoint   string
	PublicKey  string // Base64-encoded complete SSH public key blob.
	KeyFile    string
	Keyring    agent.Agent
	listener   net.Listener
	mu         sync.Mutex
	conns      map[net.Conn]struct{}
	accepted   int
	closed     int
	stopping   bool
	errs       []error
	wg         sync.WaitGroup
	ctx        context.Context
	changed    chan struct{}
	sequential bool
	checks     int
}

func NewAgent(t *testing.T) *Agent {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	public, err := ssh.NewPublicKey(&key.PublicKey)
	require.NoError(t, err)
	keyring := agent.NewKeyring()
	require.NoError(t, keyring.Add(agent.AddedKey{PrivateKey: key}))
	a := Serve(t, keyring)
	a.PublicKey = base64.StdEncoding.EncodeToString(public.Marshal())
	a.KeyFile = filepath.Join(WorkDir(t), "key")
	require.NoError(t, os.WriteFile(a.KeyFile, pem.EncodeToMemory(&pem.Block{
		Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key),
	}), 0600))
	return a
}

// Serve also supports callers that need to supply their own keyring.
func Serve(t *testing.T, keyring agent.Agent) *Agent {
	t.Helper()
	l, err := listen(t)
	require.NoError(t, err)
	a := &Agent{
		Endpoint: l.Addr().String(), Keyring: keyring, listener: l,
		conns: make(map[net.Conn]struct{}), changed: make(chan struct{}), ctx: t.Context(),
	}
	a.wg.Add(1)
	go a.run()
	t.Cleanup(func() {
		a.mu.Lock()
		a.stopping = true
		require.NoError(t, a.listener.Close())
		for c := range a.conns {
			_ = c.Close()
		}
		a.mu.Unlock()
		done := make(chan struct{})
		go func() {
			a.wg.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("SSH agent goroutines did not exit")
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		require.Empty(t, a.errs, "unexpected SSH agent server errors")
	})
	return a
}

func (a *Agent) run() {
	defer a.wg.Done()
	for {
		c, err := a.listener.Accept()
		a.mu.Lock()
		if err != nil {
			if !a.stopping {
				a.errs = append(a.errs, err)
			}
			a.mu.Unlock()
			return
		}
		if a.stopping {
			_ = c.Close()
			a.mu.Unlock()
			return
		}
		a.conns[c] = struct{}{}
		a.accepted++
		a.wg.Add(1)
		a.mu.Unlock()
		go a.serve(c)
	}
}

func (a *Agent) serve(c net.Conn) {
	defer a.wg.Done()
	a.mu.Lock()
	sequential := a.sequential
	a.mu.Unlock()
	keyring := a.Keyring
	if sequential {
		keyring = &sequentialAgent{Agent: keyring, host: a}
	}
	err := agent.ServeAgent(keyring, c)
	_ = c.Close()
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.conns, c)
	a.closed++
	close(a.changed)
	a.changed = make(chan struct{})
	if err != nil && !errors.Is(err, io.EOF) && !a.stopping {
		a.errs = append(a.errs, err)
	}
}

// RequireSequentialConnections checks disconnects while the solve is still
// running. Each new connection must wait for all preceding upstream connections
// to close before it gets its first identity reply. Session teardown therefore
// cannot hide a leak in the repeated-connection probe.
func (a *Agent) RequireSequentialConnections() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sequential = true
}

type sequentialAgent struct {
	agent.Agent
	host *Agent
	once sync.Once
	err  error
}

func (a *sequentialAgent) List() ([]*agent.Key, error) {
	a.once.Do(func() { a.err = a.host.waitPrevious() })
	if a.err != nil {
		return nil, a.err
	}
	return a.Agent.List()
}

func (a *Agent) waitPrevious() error {
	ctx, cancel := context.WithTimeoutCause(a.ctx, 10*time.Second, errors.New("previous SSH upstream connection did not close"))
	defer cancel()
	for {
		a.mu.Lock()
		if len(a.conns) == 1 {
			a.checks++
			a.mu.Unlock()
			return nil
		}
		changed := a.changed
		a.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			err := context.Cause(ctx)
			a.mu.Lock()
			a.errs = append(a.errs, err)
			a.mu.Unlock()
			return err
		}
	}
}

// Counts excludes fixture teardown: callers must assert idle before cleanup.
func (a *Agent) Counts() (accepted, active, closed int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.accepted, len(a.conns), a.closed
}

func (a *Agent) WaitIdle(t *testing.T, minimumClosed int) {
	t.Helper()
	require.Eventually(t, func() bool {
		accepted, active, closed := a.Counts()
		return accepted >= minimumClosed && active == 0 && closed == accepted
	}, 10*time.Second, 20*time.Millisecond, "SSH upstream connections did not close")
	a.mu.Lock()
	defer a.mu.Unlock()
	require.Empty(t, a.errs)
	if a.sequential {
		require.Equal(t, a.accepted, a.checks, "every upstream connection must pass the live disconnect check")
	}
}

func (a *Agent) CheckKey(t *testing.T) {
	t.Helper()
	keys, err := a.Keyring.List()
	require.NoError(t, err)
	require.Len(t, keys, 1)
	require.Equal(t, a.PublicKey, base64.StdEncoding.EncodeToString(keys[0].Blob))
}
