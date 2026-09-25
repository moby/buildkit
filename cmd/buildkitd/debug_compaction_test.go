package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/moby/buildkit/util/db"
	"github.com/moby/buildkit/util/db/compaction"
	"github.com/stretchr/testify/require"
)

type debugCompactor struct {
	calls   atomic.Int64
	compact func(context.Context, db.CompactOptions) (db.CompactResult, error)
}

func (d *debugCompactor) CompactionStats() (db.CompactionStats, error) {
	return db.CompactionStats{Size: 1024, Reclaimable: 512}, nil
}

func (d *debugCompactor) Compact(ctx context.Context, opt db.CompactOptions) (db.CompactResult, error) {
	d.calls.Add(1)
	return d.compact(ctx, opt)
}

func TestDebugCompaction(t *testing.T) {
	for name, manualOnly := range map[string]bool{"manual only": true, "automatic and manual": false} {
		t.Run(name, func(t *testing.T) {
			testDebugCompaction(t, manualOnly)
		})
	}
}

func testDebugCompaction(t *testing.T, manualOnly bool) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	d := &debugCompactor{compact: func(_ context.Context, opt db.CompactOptions) (db.CompactResult, error) {
		if opt.MinReclaimBytes != 1 || opt.MinReclaimPercent != 25 {
			t.Error("manual attempt lost policy thresholds")
		}
		select {
		case opt.Progress <- "copying":
		default:
		}
		return db.CompactResult{Compacted: true, SizeBefore: 1024, SizeAfter: 512}, nil
	}}
	cfg := compaction.DefaultConfig()
	cfg.ManualOnly = manualOnly
	cfg.MinReclaimBytes = 1
	cfg.IdleTimeout = time.Millisecond
	s, err := compaction.NewFile(cfg, path, true, d)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	mux := http.NewServeMux()
	mux.HandleFunc("GET /debug/compaction", handleCompactionStatus)
	mux.HandleFunc("POST /debug/compaction", handleCompactionRequest)
	server := httptest.NewServer(mux)
	defer server.Close()
	client := server.Client()
	client.Timeout = 5 * time.Second
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+"/debug/compaction", nil)
	require.NoError(t, err)
	resp, err := client.Do(req)
	require.NoError(t, err)
	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(data), "eligible for a manual attempt after idle")
	require.Zero(t, d.calls.Load())
	req, err = http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/debug/compaction?database=unknown", nil)
	require.NoError(t, err)
	resp, err = client.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	require.NoError(t, resp.Body.Close())
	req, err = http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/debug/compaction?database="+url.QueryEscape(path), nil)
	require.NoError(t, err)
	resp, err = client.Do(req)
	require.NoError(t, err)
	data, err = io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(data), "waiting for idle")
	require.Contains(t, string(data), "copying")
	require.Contains(t, string(data), "compacted=true")
	require.Equal(t, int64(1), d.calls.Load())
	require.NoError(t, s.Close())
	_, registered := compaction.Files()[path]
	require.False(t, registered)
}

func TestDebugCompactionDisconnect(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	started, canceled := make(chan struct{}), make(chan struct{})
	d := &debugCompactor{compact: func(ctx context.Context, _ db.CompactOptions) (db.CompactResult, error) {
		close(started)
		<-ctx.Done()
		close(canceled)
		return db.CompactResult{}, context.Cause(ctx)
	}}
	cfg := compaction.DefaultConfig()
	cfg.IdleTimeout = time.Millisecond
	s, err := compaction.NewFile(cfg, path, true, d)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	server := httptest.NewServer(http.HandlerFunc(handleCompactionRequest))
	defer server.Close()
	client := server.Client()
	client.Timeout = 5 * time.Second
	endpoint := server.URL + "?database=" + url.QueryEscape(path)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, endpoint, nil)
	require.NoError(t, err)
	resp, err := client.Do(req)
	require.NoError(t, err)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("copy did not start")
	}
	req, err = http.NewRequestWithContext(t.Context(), http.MethodPost, endpoint, nil)
	require.NoError(t, err)
	duplicate, err := client.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusConflict, duplicate.StatusCode)
	require.NoError(t, duplicate.Body.Close())
	require.NoError(t, resp.Body.Close())
	select {
	case <-canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("disconnect did not cancel copy")
	}
}

func TestDebugCompactionOrigin(t *testing.T) {
	for _, tc := range []struct {
		name   string
		origin string
		site   string
		status int
	}{
		{name: "CLI", status: http.StatusNotFound},
		{name: "same origin", origin: "http://localhost:6060", status: http.StatusNotFound},
		{name: "foreign origin", origin: "https://example.com", status: http.StatusForbidden},
		{name: "cross site", site: "cross-site", status: http.StatusForbidden},
		{name: "same site", site: "same-site", status: http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "http://localhost:6060/debug/compaction?database=unknown", nil)
			r.Header.Set("Origin", tc.origin)
			r.Header.Set("Sec-Fetch-Site", tc.site)
			w := httptest.NewRecorder()
			handleCompactionRequest(w, r)
			require.Equal(t, tc.status, w.Code)
		})
	}
}
