package retryhandler

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"testing"
	"testing/synctest"
	"time"

	remoteserrors "github.com/containerd/containerd/v2/core/remotes/errors"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestWithRetry(t *testing.T) {
	reset := &url.Error{Op: http.MethodGet, URL: "https://registry.example/token", Err: &net.OpError{
		Op: "read", Net: "tcp", Err: errConnectionReset,
	}}
	for _, tc := range []struct {
		name     string
		err      error
		attempts int
	}{
		{
			name:     "reset",
			err:      reset,
			attempts: 4,
		},
		{
			name:     "unexpected eof",
			err:      io.ErrUnexpectedEOF,
			attempts: 4,
		},
		{
			name:     "server error",
			err:      remoteserrors.ErrUnexpectedStatus{StatusCode: http.StatusServiceUnavailable},
			attempts: 4,
		},
		{
			name:     "forbidden",
			err:      remoteserrors.ErrUnexpectedStatus{StatusCode: http.StatusForbidden},
			attempts: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				attempts := 0
				start := time.Now()
				previous := start
				_, err := WithRetry(t.Context(), nil, func(context.Context) (int, error) {
					if attempts > 0 {
						backoff := time.Second << (attempts - 1)
						require.GreaterOrEqual(t, time.Since(previous), backoff/2)
						require.Less(t, time.Since(previous), backoff)
					}
					previous = time.Now()
					attempts++
					return 0, tc.err
				})
				require.Equal(t, tc.err, err)
				require.Equal(t, tc.attempts, attempts)
				if tc.attempts > 1 {
					require.GreaterOrEqual(t, time.Since(start), 3500*time.Millisecond)
					require.Less(t, time.Since(start), 7*time.Second)
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
		require.GreaterOrEqual(t, time.Since(start), 3500*time.Millisecond)
		require.Less(t, time.Since(start), 7*time.Second)
	})
}

func TestWithRetryCancellationDuringBackoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancelCause(t.Context())
		defer cancel(context.Canceled)
		cancelCause := errors.New("canceled by caller")
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
		cancel(cancelCause)
		err := <-done
		require.ErrorIs(t, err, context.Canceled)
		require.ErrorIs(t, err, cancelCause)
		require.Equal(t, 1, attempts)
		require.Zero(t, time.Since(start))
	})
}

func TestWithRetryAfter(t *testing.T) {
	for _, tc := range []struct {
		name     string
		delay    time.Duration
		veto     bool
		attempts int
		elapsed  time.Duration
	}{
		{
			name:     "server minimum",
			delay:    7 * time.Second,
			attempts: 4,
			elapsed:  21 * time.Second,
		},
		{
			name:     "at exponential backoff limit",
			delay:    MaxRetryBackoff,
			attempts: 4,
			elapsed:  3 * MaxRetryBackoff,
		},
		{
			name:     "above exponential backoff limit",
			delay:    10 * time.Second,
			attempts: 4,
			elapsed:  30 * time.Second,
		},
		{
			name:     "at server delay limit",
			delay:    MaxRetryAfter,
			attempts: 4,
			elapsed:  3 * MaxRetryAfter,
		},
		{
			name:     "just above server delay limit",
			delay:    MaxRetryAfter + time.Nanosecond,
			attempts: 1,
		},
		{
			name:     "above limit",
			delay:    time.Hour,
			attempts: 1,
		},
		{
			name:     "predicate veto",
			delay:    time.Second,
			veto:     true,
			attempts: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				cause := errors.New("retry later")
				failure := errors.Wrap(&RetryAfterError{Err: cause, Delay: tc.delay}, "request failed")
				attempts := 0
				start := time.Now()
				_, err := WithRetryIf(t.Context(), nil, func(ctx context.Context) (int, error) {
					return WithRetry(ctx, nil, func(context.Context) (int, error) {
						attempts++
						return 0, failure
					})
				}, func(error) bool { return !tc.veto })
				require.ErrorIs(t, err, cause)
				require.Equal(t, failure.Error(), err.Error())
				require.Equal(t, tc.attempts, attempts)
				require.Equal(t, tc.elapsed, time.Since(start))
			})
		})
	}
}
