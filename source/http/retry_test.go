package http

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

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

// TestDoRequestDoesNotRetryStatusErrors asserts the retry does not change how
// HTTP status codes are handled: the response is returned to the caller on the
// first attempt, exactly as before.
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
