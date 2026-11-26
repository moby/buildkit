package sshutil

import (
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A server that accepts the connection but never speaks SSH must not block the
// key scan indefinitely.
func TestSSHKeyScanUnresponsiveServer(t *testing.T) {
	var listenConfig net.ListenConfig
	listener, err := listenConfig.Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()

	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			accepted <- conn
		}
	}()
	defer func() {
		select {
		case conn := <-accepted:
			conn.Close()
		default:
		}
	}()

	done := make(chan error, 1)
	go func() {
		_, err := SSHKeyScan(listener.Addr().String())
		done <- err
	}()

	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(30 * time.Second):
		t.Fatal("SSHKeyScan did not return for an unresponsive server")
	}
}
