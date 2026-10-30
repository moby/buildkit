package gateway

import (
	"context"
	"testing"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

func TestServeCancelBeforeFrontendStartup(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(errors.WithStack(context.Canceled))
	p := newPipe()
	t.Cleanup(func() { p.conn.Close() })
	server := grpc.NewServer()
	t.Cleanup(server.Stop)
	done := make(chan struct{})
	go func() {
		serve(ctx, server, p.conn)
		close(done)
	}()
	cancel(errors.WithStack(context.Canceled))
	receiveGatewayLifecycleTest(t, done, "server cancellation before frontend startup")
}

func TestServeFrontendClosesBeforeStartup(t *testing.T) {
	t.Parallel()
	p := newPipe()
	t.Cleanup(func() { p.conn.Close() })
	server := grpc.NewServer()
	t.Cleanup(server.Stop)
	done := make(chan struct{})
	go func() {
		serve(t.Context(), server, p.conn)
		close(done)
	}()
	require.NoError(t, p.Stdout.Close())
	receiveGatewayLifecycleTest(t, done, "server shutdown on frontend EOF")
}
