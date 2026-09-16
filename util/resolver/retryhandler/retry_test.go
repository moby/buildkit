package retryhandler

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/url"
	"testing"
	"testing/synctest"
	"time"

	remoteserrors "github.com/containerd/containerd/v2/core/remotes/errors"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

func TestRetryConnectionReset(t *testing.T) {
	err := &url.Error{Op: http.MethodGet, URL: "https://registry.example/token", Err: &net.OpError{
		Op: "read", Net: "tcp", Err: errConnectionReset,
	}}
	require.True(t, retryError(err))
}

func TestWithRetry(t *testing.T) {
	for _, tc := range []struct {
		name     string
		err      error
		attempts int
	}{
		{"reset", errConnectionReset, 4},
		{"eof", io.EOF, 4},
		{"server error", remoteserrors.ErrUnexpectedStatus{StatusCode: http.StatusServiceUnavailable}, 4},
		{"forbidden", remoteserrors.ErrUnexpectedStatus{StatusCode: http.StatusForbidden}, 1},
		{"unauthorized", remoteserrors.ErrUnexpectedStatus{StatusCode: http.StatusUnauthorized}, 1},
		{"rate limited", remoteserrors.ErrUnexpectedStatus{StatusCode: http.StatusTooManyRequests}, 1},
		{"canceled", context.Canceled, 1},
		{"untrusted certificate", &url.Error{Op: http.MethodGet, URL: "https://registry.example/token", Err: &tls.CertificateVerificationError{
			Err: x509.UnknownAuthorityError{Cert: &x509.Certificate{}},
		}}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				attempts := 0
				start := time.Now()
				_, err := WithRetry(t.Context(), nil, func(context.Context) (int, error) {
					attempts++
					return 0, tc.err
				})
				require.Equal(t, tc.err, err)
				require.Equal(t, tc.attempts, attempts)
				if tc.attempts > 1 {
					require.Equal(t, 7*time.Second, time.Since(start))
				} else {
					require.Zero(t, time.Since(start))
				}
			})
		})
	}
}

func TestWithRetryNestedHandler(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		attempts := 0
		handler := New(func(ctx context.Context, _ ocispecs.Descriptor) ([]ocispecs.Descriptor, error) {
			return WithRetry(ctx, nil, func(context.Context) ([]ocispecs.Descriptor, error) {
				attempts++
				return nil, io.EOF
			})
		}, nil)
		start := time.Now()
		_, err := handler(t.Context(), ocispecs.Descriptor{})
		require.ErrorIs(t, err, io.EOF)
		require.Equal(t, 4, attempts)
		require.Equal(t, 7*time.Second, time.Since(start))
	})
}

func TestWithRetryCancellationDuringBackoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancelCause(t.Context())
		defer cancel(context.Canceled)
		attempts := 0
		done := make(chan error, 1)
		go func() {
			_, err := WithRetry(ctx, nil, func(context.Context) (int, error) {
				attempts++
				return 0, io.EOF
			})
			done <- err
		}()
		synctest.Wait()
		start := time.Now()
		cancel(context.Canceled)
		require.ErrorIs(t, <-done, io.EOF)
		require.Equal(t, 1, attempts)
		require.Zero(t, time.Since(start))
	})
}
