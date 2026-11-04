package http

import (
	"context"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/moby/buildkit/util/resolver/retryhandler"
	"github.com/stretchr/testify/require"
)

// TestDoRequestRetriesDroppedConnection covers a connection dropped before any
// response is written, which reaches the client as EOF. A solve with many HTTP
// sources hits this regularly on a lossy network, and before the retry a
// single occurrence failed the whole build.
func TestDoRequestRetriesDroppedConnection(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) <= 2 {
			hj, ok := w.(http.Hijacker)
			if !ok {
				return
			}
			conn, _, err := hj.Hijack()
			if err != nil {
				return
			}
			conn.Close()
			return
		}
		w.Write([]byte("content"))
	}))
	defer server.Close()

	ctx := t.Context()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	require.NoError(t, err)

	resp, err := doRequest(ctx, server.Client(), req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, int32(3), attempts.Load())
}

// TestDoRequestDoesNotRetryStatusErrors checks a permanent HTTP failure.
func TestDoRequestDoesNotRetryStatusErrors(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	ctx := t.Context()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	require.NoError(t, err)

	resp, err := doRequest(ctx, server.Client(), req)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	require.Equal(t, int32(1), attempts.Load())
}

func TestDoRequestRetryAfter(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable} {
			t.Run(method+"/"+http.StatusText(status), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					attempts := 0
					start := time.Now()
					failedBody := &retryTestBody{Reader: strings.NewReader("retry later")}
					successBody := &retryTestBody{Reader: strings.NewReader("content")}
					client := &http.Client{Transport: retryTestTransport(func(req *http.Request) (*http.Response, error) {
						attempts++
						require.Equal(t, method, req.Method)
						require.Equal(t, "etag", req.Header.Get("If-None-Match"))
						if attempts == 1 {
							return &http.Response{
								StatusCode: status,
								Header:     http.Header{"Retry-After": {"10"}},
								Body:       failedBody,
							}, nil
						}
						require.True(t, failedBody.closed)
						require.Equal(t, len("retry later"), failedBody.Len())
						require.Equal(t, 10*time.Second, time.Since(start))
						return &http.Response{StatusCode: http.StatusOK, Body: successBody}, nil
					})}
					req, err := http.NewRequestWithContext(t.Context(), method, "https://example.com", nil)
					require.NoError(t, err)
					req.Header.Set("If-None-Match", "etag")
					resp, err := doRequest(t.Context(), client, req)
					require.NoError(t, err)
					defer resp.Body.Close()
					require.Equal(t, 2, attempts)
					require.False(t, successBody.closed)
					body, err := io.ReadAll(resp.Body)
					require.NoError(t, err)
					require.Equal(t, "content", string(body))
				})
			})
		}
	}
}

func TestDoRequestStatusRetryLimit(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		retryAfter string
		attempts   int
		wantError  bool
	}{
		{
			name:      "rate limited",
			status:    http.StatusTooManyRequests,
			attempts:  4,
			wantError: true,
		},
		{
			name:       "unavailable with delay",
			status:     http.StatusServiceUnavailable,
			retryAfter: "3",
			attempts:   4,
			wantError:  true,
		},
		{
			name:       "delay exceeds limit",
			status:     http.StatusTooManyRequests,
			retryAfter: "3600",
			attempts:   1,
			wantError:  true,
		},
		{
			name:       "other status ignores retry after",
			status:     http.StatusInternalServerError,
			retryAfter: "3",
			attempts:   1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				attempts := 0
				start := time.Now()
				var bodies []*retryTestBody
				client := &http.Client{Transport: retryTestTransport(func(*http.Request) (*http.Response, error) {
					attempts++
					body := &retryTestBody{Reader: strings.NewReader("error")}
					bodies = append(bodies, body)
					return &http.Response{
						StatusCode: tc.status,
						Header:     http.Header{"Retry-After": {tc.retryAfter}},
						Body:       body,
					}, nil
				})}
				req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://example.com", nil)
				require.NoError(t, err)
				resp, err := doRequest(t.Context(), client, req)
				if tc.wantError {
					var retryAfter *retryhandler.RetryAfterError
					require.ErrorAs(t, err, &retryAfter)
					require.Nil(t, resp)
					for _, body := range bodies {
						require.True(t, body.closed)
					}
				} else {
					require.NoError(t, err)
					require.Equal(t, tc.status, resp.StatusCode)
					require.False(t, bodies[0].closed)
					resp.Body.Close()
				}
				require.Equal(t, tc.attempts, attempts)
				if tc.attempts == 1 {
					require.Zero(t, time.Since(start))
				}
			})
		})
	}
}

func TestDoRequestRetryAfterCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancelCause(t.Context())
		defer cancel(context.Canceled)
		attempts := 0
		body := &retryTestBody{Reader: strings.NewReader("retry later")}
		client := &http.Client{Transport: retryTestTransport(func(*http.Request) (*http.Response, error) {
			attempts++
			return &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Header:     http.Header{"Retry-After": {"30"}},
				Body:       body,
			}, nil
		})}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://example.com", nil)
		require.NoError(t, err)
		done := make(chan error, 1)
		go func() {
			resp, err := doRequest(ctx, client, req)
			if resp != nil {
				resp.Body.Close()
			}
			done <- err
		}()
		synctest.Wait()
		require.True(t, body.closed)
		start := time.Now()
		cancel(context.Canceled)
		require.ErrorIs(t, <-done, context.Canceled)
		require.Zero(t, time.Since(start))
		require.Equal(t, 1, attempts)
	})
}

func TestRetryAfterDelay(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			value string
			want  time.Duration
		}{
			{
				name:  "seconds",
				value: "3",
				want:  3 * time.Second,
			},
			{
				name:  "date",
				value: time.Now().Add(5 * time.Second).UTC().Format(http.TimeFormat),
				want:  5 * time.Second,
			},
			{
				name:  "past date",
				value: time.Now().Add(-time.Minute).UTC().Format(http.TimeFormat),
			},
			{
				name: "missing",
			},
			{
				name:  "zero",
				value: "0",
			},
			{
				name:  "invalid",
				value: "tomorrow",
			},
			{
				name:  "negative",
				value: "-1",
			},
			{
				name:  "duration overflow",
				value: "9223372037",
				want:  time.Duration(math.MaxInt64),
			},
			{
				name:  "integer overflow",
				value: "18446744073709551616",
				want:  time.Duration(math.MaxInt64),
			},
		} {
			require.Equal(t, tc.want, retryAfterDelay(tc.value), tc.name)
		}
	})
}

type retryTestTransport func(*http.Request) (*http.Response, error)

func (f retryTestTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type retryTestBody struct {
	*strings.Reader
	closed bool
}

func (b *retryTestBody) Close() error {
	b.closed = true
	return nil
}
