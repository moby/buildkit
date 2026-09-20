package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHandleReleaseUnreferenced(t *testing.T) {
	called := false
	h := func(w http.ResponseWriter, r *http.Request) {
		handleReleaseUnreferenced(w, r, func(ctx context.Context) error {
			called = true
			require.NoError(t, ctx.Err())
			return nil
		})
	}

	req := httptest.NewRequest(http.MethodPost, "/debug/cache/release-unreferenced", nil)
	resp := httptest.NewRecorder()
	h(resp, req)

	require.True(t, called)
	require.Equal(t, http.StatusNoContent, resp.Code)
}

func TestHandleReleaseUnreferencedMethodNotAllowed(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/debug/cache/release-unreferenced", nil)
	resp := httptest.NewRecorder()
	handleReleaseUnreferenced(resp, req, func(context.Context) error {
		t.Fatal("release callback should not be called")
		return nil
	})

	require.Equal(t, http.StatusMethodNotAllowed, resp.Code)
	require.Equal(t, http.MethodPost, resp.Header().Get("Allow"))
}

func TestHandleReleaseUnreferencedNotInitialized(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/debug/cache/release-unreferenced", nil)
	resp := httptest.NewRecorder()
	handleReleaseUnreferenced(resp, req, nil)

	require.Equal(t, http.StatusServiceUnavailable, resp.Code)
}

func TestHandleReleaseUnreferencedError(t *testing.T) {
	expectedErr := errors.New("release failed")
	req := httptest.NewRequest(http.MethodPost, "/debug/cache/release-unreferenced", nil)
	resp := httptest.NewRecorder()
	handleReleaseUnreferenced(resp, req, func(context.Context) error {
		return expectedErr
	})

	require.Equal(t, http.StatusInternalServerError, resp.Code)
	require.Contains(t, resp.Body.String(), expectedErr.Error())
}

func TestHandleReleaseUnreferencedContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/debug/cache/release-unreferenced", nil)
	resp := httptest.NewRecorder()
	handleReleaseUnreferenced(resp, req, func(got context.Context) error {
		require.Equal(t, req.Context(), got)
		return got.Err()
	})
	require.Equal(t, http.StatusInternalServerError, resp.Code)
	require.Contains(t, resp.Body.String(), context.Canceled.Error())
}
