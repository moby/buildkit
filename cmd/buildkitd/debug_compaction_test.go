package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/moby/buildkit/util/db"
	"github.com/moby/buildkit/util/db/boltutil"
	"github.com/moby/buildkit/util/db/compaction"
	"github.com/stretchr/testify/require"
	bolt "go.etcd.io/bbolt"
)

type debugCompactor struct {
	calls   atomic.Int64
	stats   db.CompactionStats
	compact func(context.Context, db.CompactOptions) (db.CompactResult, error)
}

func (d *debugCompactor) CompactionStats() (db.CompactionStats, error) {
	if d.stats.Size != 0 {
		return d.stats, nil
	}
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

func TestDebugCompactionStatusBelowPercentFloor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	d := &debugCompactor{stats: db.CompactionStats{Size: 100 << 30, Reclaimable: 256 << 20}}
	cfg := compaction.DefaultConfig()
	cfg.ManualOnly = true
	s, err := compaction.NewFile(cfg, path, true, d)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })

	w := httptest.NewRecorder()
	handleCompactionStatus(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/debug/compaction", nil))
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), "reclaimable space below threshold")
}

func TestDebugCompactionFragmentation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	d := &debugCompactor{compact: func(_ context.Context, opt db.CompactOptions) (db.CompactResult, error) {
		if !opt.CheckFragmentation || opt.MinReclaimPercentFloor != 10 {
			t.Error("fragmentation check lost its option or policy thresholds")
		}
		return db.CompactResult{Reason: "trial copy below threshold"}, nil
	}}
	cfg := compaction.DefaultConfig()
	cfg.ManualOnly = true
	cfg.IdleTimeout = time.Millisecond
	s, err := compaction.NewFile(cfg, path, true, d)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	endpoint := "/debug/compaction?database=" + url.QueryEscape(path) + "&check-fragmentation="
	w := httptest.NewRecorder()
	handleCompactionRequest(w, httptest.NewRequestWithContext(t.Context(), http.MethodPost, endpoint+"invalid", nil))
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Zero(t, d.calls.Load())
	server := httptest.NewServer(http.HandlerFunc(handleCompactionRequest))
	defer server.Close()
	client := server.Client()
	client.Timeout = 5 * time.Second
	r, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+endpoint+"true", nil)
	require.NoError(t, err)
	resp, err := client.Do(r)
	require.NoError(t, err)
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, string(data), "trial copy below threshold")
	require.Equal(t, int64(1), d.calls.Load())
}

func TestDebugCompactionFragmentationDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	cfg := compaction.DefaultConfig()
	cfg.ManualOnly = true
	cfg.MinReclaimBytes = 1 << 20
	cfg.IdleTimeout = time.Millisecond
	d, err := boltutil.Open(path, 0600, &bolt.Options{PageSize: 4096, NoFreelistSync: true}, cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, d.Close()) })
	value := bytes.Repeat([]byte("x"), 256)
	const count = 6000
	require.NoError(t, d.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucket([]byte("records"))
		if err != nil {
			return err
		}
		b.FillPercent = 0.3
		for i := range count {
			if err := b.Put(fmt.Appendf(nil, "%08d", i), value); err != nil {
				return err
			}
		}
		return b.SetSequence(42)
	}))
	mux := http.NewServeMux()
	mux.HandleFunc("GET /debug/compaction", handleCompactionStatus)
	mux.HandleFunc("POST /debug/compaction", handleCompactionRequest)
	server := httptest.NewServer(mux)
	defer server.Close()
	client := server.Client()
	client.Timeout = 10 * time.Second
	endpoint := server.URL + "/debug/compaction?database=" + url.QueryEscape(path)
	request := func(method, endpoint string) string {
		t.Helper()
		r, err := http.NewRequestWithContext(t.Context(), method, endpoint, nil)
		require.NoError(t, err)
		resp, err := client.Do(r)
		require.NoError(t, err)
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		return string(body)
	}
	require.Contains(t, request(http.MethodGet, endpoint), "reclaimable space below threshold")
	body := request(http.MethodPost, endpoint)
	require.Contains(t, body, "compacted=false")
	require.NotContains(t, body, "inspecting page fragmentation")
	body = request(http.MethodPost, endpoint+"&check-fragmentation=true")
	require.Contains(t, body, "inspecting page fragmentation")
	require.Contains(t, body, "fragmentation inspection complete")
	require.Contains(t, body, "compacted=true")
	require.Contains(t, body, `error=""`)
	require.NoFileExists(t, path+".compact")
	body = request(http.MethodPost, endpoint+"&check-fragmentation=true")
	require.Contains(t, body, "compacted=false")
	require.NotContains(t, body, "copying and replacing database")
	require.NoError(t, d.Close())
	reopened, err := boltutil.Open(path, 0600, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })
	require.NoError(t, reopened.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("records"))
		require.NotNil(t, b)
		require.Equal(t, uint64(42), b.Sequence())
		require.Equal(t, count, b.Stats().KeyN)
		for i := range count {
			require.Equal(t, value, b.Get(fmt.Appendf(nil, "%08d", i)))
		}
		for err := range tx.Check() {
			require.NoError(t, err)
		}
		return nil
	}))
	require.NoError(t, reopened.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte("records")).Put([]byte("after-reopen"), value)
	}))
}

func testDebugCompaction(t *testing.T, manualOnly bool) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	d := &debugCompactor{compact: func(_ context.Context, opt db.CompactOptions) (db.CompactResult, error) {
		if opt.MinReclaimBytes != 1 || opt.MinReclaimPercent != 30 || opt.MinReclaimPercentFloor != 10 || opt.CheckFragmentation {
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
