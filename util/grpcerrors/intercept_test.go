package grpcerrors_test

import (
	"context"
	"net"
	"syscall"
	"testing"
	"time"

	"github.com/moby/buildkit/errdefs"
	"github.com/moby/buildkit/util/grpcerrors"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	health "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
)

func TestUnaryClientCancellationCause(t *testing.T) {
	t.Parallel()
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	server := grpc.NewServer()
	started := make(chan struct{})
	health.RegisterHealthServer(server, &blockingHealthServer{started: started})
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient("passthrough:///test",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", listener.Addr().String())
		}),
		grpc.WithUnaryInterceptor(grpcerrors.UnaryClientInterceptor),
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, conn.Close()) })

	// Only the derived RPC context is canceled, as with session health failures.
	parent := t.Context()
	ctx, cancel := context.WithCancelCause(parent)
	defer cancel(context.Canceled)
	cause := errors.New("session healthcheck failed fatally")
	result := make(chan error, 1)
	go func() { _, err := health.NewHealthClient(conn).Check(ctx, &health.HealthCheckRequest{}); result <- err }()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("RPC did not start")
	}
	cancel(cause)
	select {
	case err := <-result:
		require.ErrorIs(t, err, cause)
		require.Equal(t, codes.Canceled, grpcerrors.Code(err))
		encoded := grpcerrors.ToGRPC(parent, err)
		require.Equal(t, codes.Canceled, status.Code(encoded))
		require.Contains(t, status.Convert(encoded).Message(), cause.Error())
		require.NoError(t, context.Cause(parent))
	case <-time.After(10 * time.Second):
		t.Fatal("RPC did not stop")
	}
}

type blockingHealthServer struct {
	health.UnimplementedHealthServer
	started chan struct{}
}

func (s *blockingHealthServer) Check(ctx context.Context, _ *health.HealthCheckRequest) (*health.HealthCheckResponse, error) {
	close(s.started)
	<-ctx.Done()
	return nil, context.Cause(ctx)
}

func TestUnaryClientPreservesRPCError(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(errors.New("local cancellation"))
	for _, tc := range []struct {
		name string
		err  error
	}{
		{
			name: "success",
		},
		{
			name: "unrelated status",
			err:  status.Error(codes.PermissionDenied, "denied"),
		},
		{
			name: "remote cancellation",
			err:  status.Error(codes.Canceled, "remote operation canceled"),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := grpcerrors.UnaryClientInterceptor(ctx, "/test", nil, nil, nil, func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error { return tc.err })
			if tc.err == nil {
				require.NoError(t, err)
				return
			}
			require.Equal(t, status.Code(tc.err), grpcerrors.Code(err))
			require.Contains(t, err.Error(), status.Convert(tc.err).Message())
		})
	}
}

func TestUnaryClientDeadlineCause(t *testing.T) {
	t.Parallel()
	cause := errors.New("frontend startup timed out")
	ctx, cancel := context.WithTimeoutCause(t.Context(), -time.Second, cause)
	defer cancel()
	rpcErr := status.Error(codes.DeadlineExceeded, "context deadline exceeded")
	invoke := func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error { return rpcErr }
	err := grpcerrors.UnaryClientInterceptor(ctx, "/test", nil, nil, nil, invoke)
	require.ErrorIs(t, err, cause)
	require.Equal(t, codes.DeadlineExceeded, grpcerrors.Code(err))
	encoded := grpcerrors.ToGRPC(t.Context(), err)
	require.Equal(t, codes.DeadlineExceeded, status.Code(encoded))
	require.Equal(t, cause.Error(), status.Convert(encoded).Message())

	// A remote deadline must remain intact while the local context is live.
	err = grpcerrors.UnaryClientInterceptor(t.Context(), "/test", nil, nil, nil, invoke)
	require.Equal(t, codes.DeadlineExceeded, grpcerrors.Code(err))
	require.NotErrorIs(t, err, cause)
	require.Contains(t, err.Error(), "context deadline exceeded")
}

func TestUnaryClientCauseStatus(t *testing.T) {
	t.Parallel()
	for _, cause := range []error{context.Canceled, errors.New("custom"), errdefs.Internal(errors.New("internal")), errors.WithStack(syscall.ENOSPC), status.Error(codes.Unavailable, "unavailable")} {
		t.Run(cause.Error(), func(t *testing.T) {
			ctx, cancel := context.WithCancelCause(t.Context())
			cancel(cause)
			err := grpcerrors.UnaryClientInterceptor(ctx, "/test", nil, nil, nil, func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
				return status.Error(codes.Canceled, context.Canceled.Error())
			})
			require.ErrorIs(t, err, cause)
			require.Equal(t, codes.Canceled, grpcerrors.Code(err))
			require.Equal(t, codes.Canceled, status.Code(err))
			require.Equal(t, codes.Canceled, status.Code(grpcerrors.ToGRPC(t.Context(), err)))
		})
	}
}

func TestUnaryClientDelayedDeadline(t *testing.T) {
	t.Parallel()
	cause := errors.New("auth deadline")
	base, cancel := context.WithCancelCause(t.Context())
	defer cancel(context.Canceled)
	ctx := delayedDeadlineContext{base}
	err := grpcerrors.UnaryClientInterceptor(ctx, "/test", nil, nil, nil, func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
		require.NoError(t, context.Cause(ctx))
		time.AfterFunc(10*time.Millisecond, func() { cancel(cause) })
		return status.Error(codes.DeadlineExceeded, context.DeadlineExceeded.Error())
	})
	require.ErrorIs(t, err, cause)
	require.Equal(t, codes.DeadlineExceeded, grpcerrors.Code(err))
	require.Equal(t, codes.DeadlineExceeded, status.Code(err))
}

type delayedDeadlineContext struct{ context.Context }

func (delayedDeadlineContext) Deadline() (time.Time, bool) {
	return time.Now().Add(-time.Second), true
}

func (c delayedDeadlineContext) Err() error {
	select {
	case <-c.Done():
		return context.DeadlineExceeded
	default:
		return nil
	}
}
