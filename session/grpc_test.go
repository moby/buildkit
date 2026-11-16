package session

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
)

func TestSessionDisconnectCleanup(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(context.Canceled)
	manager, err := NewManager()
	require.NoError(t, err)

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()
	server := grpc.NewServer()
	defer server.Stop()
	grpc_health_v1.RegisterHealthServer(server, health.NewServer())
	go serve(ctx, server, serverConn)

	done := make(chan error, 1)
	go func() {
		done <- manager.HandleConn(ctx, clientConn, map[string][]string{
			headerSessionID:                  {"disconnect-test"},
			headerSessionHealthCustomTimeout: {"1000"},
		})
	}()

	checkCtx, checkCancel := context.WithTimeoutCause(ctx, 10*time.Second, context.DeadlineExceeded)
	defer checkCancel()
	caller, err := manager.Get(checkCtx, "disconnect-test", false)
	require.NoError(t, err)
	require.NotNil(t, caller)
	cc := caller.Conn()
	callerCtx := caller.Context(ctx)
	_, err = grpc_health_v1.NewHealthClient(cc).Check(checkCtx, &grpc_health_v1.HealthCheckRequest{}, grpc.WaitForReady(true))
	require.NoError(t, err)

	// Break an established transport without canceling the handler's context.
	require.NoError(t, serverConn.Close())
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("session handler did not exit after disconnect")
	}
	require.NoError(t, context.Cause(ctx))
	select {
	case <-callerCtx.Done():
		require.ErrorContains(t, context.Cause(callerCtx), "session healthcheck failed fatally")
	case <-time.After(5 * time.Second):
		t.Fatal("session context was not canceled")
	}
	caller, err = manager.Get(ctx, "disconnect-test", true)
	require.NoError(t, err)
	require.Nil(t, caller)
	require.Eventually(t, func() bool {
		return cc.GetState() == connectivity.Shutdown
	}, 5*time.Second, 10*time.Millisecond)
}
