package auth

import (
	"context"
	"testing"
	"time"

	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/testutil"
	"github.com/moby/buildkit/util/grpcerrors"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestSessionAuthTimeout(t *testing.T) {
	original := SessionAuthTimeout
	SessionAuthTimeout = 100 * time.Millisecond
	t.Cleanup(func() { SessionAuthTimeout = original })

	sm, err := session.NewManager()
	require.NoError(t, err)

	s, err := session.NewSession(t.Context(), "")
	require.NoError(t, err)

	started := make(chan struct{}, 1)
	s.Allow(blockingAuthServer{started: started})
	done := make(chan error, 1)
	go func() { done <- s.Run(t.Context(), session.Dialer(testutil.TestStream(sm.HandleConn))) }()
	t.Cleanup(func() { require.NoError(t, s.Close()); require.NoError(t, <-done) })

	ctx, cancel := context.WithTimeoutCause(t.Context(), 10*time.Second, errors.New("waiting for auth session"))
	defer cancel()

	_, err = sm.Get(ctx, s.ID(), false)
	require.NoError(t, err)

	g := session.NewGroup(s.ID())
	for _, tc := range []struct {
		name string
		call func(context.Context) error
	}{
		{
			name: "resolving credentials from session",
			call: func(ctx context.Context) error {
				_, _, _, err := CredentialsFunc(ctx, sm, g)("registry.example.com")
				return err
			},
		},
		{
			name: "fetching auth token from session",
			call: func(ctx context.Context) error { _, err := FetchToken(ctx, &FetchTokenRequest{}, sm, g); return err },
		},
		{
			name: "getting token authority from session",
			call: func(ctx context.Context) error {
				_, _, err := GetTokenAuthority(ctx, "registry.example.com", sm, g)
				return err
			},
		},
		{
			name: "verifying token authority from session",
			call: func(ctx context.Context) error {
				_, _, err := VerifyTokenAuthority(ctx, "registry.example.com", new([32]byte), sm, g)
				return err
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call(t.Context())
			require.ErrorIs(t, err, context.DeadlineExceeded)
			require.EqualError(t, err, tc.name+": context deadline exceeded")
			encoded := grpcerrors.ToGRPC(t.Context(), err)
			require.Equal(t, codes.DeadlineExceeded, status.Code(encoded))
			require.Equal(t, err.Error(), status.Convert(encoded).Message())

			select {
			case <-started:
			case <-time.After(10 * time.Second):
				t.Fatal("timeout RPC did not reach the handler")
			}
			parent, cancel := context.WithCancelCause(t.Context())
			cause := errors.Wrap(context.Canceled, "build canceled")
			go func() {
				select {
				case <-started:
					cancel(cause)
				case <-parent.Done():
				}
			}()
			defer cancel(context.Canceled)
			err = tc.call(parent)
			require.ErrorIs(t, err, cause)
			require.Equal(t, codes.Canceled, grpcerrors.Code(err))
			require.NotContains(t, err.Error(), tc.name)
		})
	}
}

type blockingAuthServer struct {
	UnimplementedAuthServer
	started chan struct{}
}

func (s blockingAuthServer) Register(server *grpc.Server) {
	RegisterAuthServer(server, s)
}

func (s blockingAuthServer) Credentials(ctx context.Context, _ *CredentialsRequest) (*CredentialsResponse, error) {
	s.started <- struct{}{}
	<-ctx.Done()
	return nil, context.Cause(ctx)
}

func (s blockingAuthServer) FetchToken(ctx context.Context, _ *FetchTokenRequest) (*FetchTokenResponse, error) {
	s.started <- struct{}{}
	<-ctx.Done()
	return nil, context.Cause(ctx)
}

func (s blockingAuthServer) GetTokenAuthority(ctx context.Context, _ *GetTokenAuthorityRequest) (*GetTokenAuthorityResponse, error) {
	s.started <- struct{}{}
	<-ctx.Done()
	return nil, context.Cause(ctx)
}

func (s blockingAuthServer) VerifyTokenAuthority(ctx context.Context, _ *VerifyTokenAuthorityRequest) (*VerifyTokenAuthorityResponse, error) {
	s.started <- struct{}{}
	<-ctx.Done()
	return nil, context.Cause(ctx)
}
