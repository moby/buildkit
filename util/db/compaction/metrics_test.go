package compaction

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"github.com/moby/buildkit/util/db"
	"github.com/pkg/errors"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/stretchr/testify/require"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func testMetrics(t *testing.T) (*Metrics, *sdkmetric.ManualReader) {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { require.NoError(t, mp.Shutdown(context.WithoutCancel(t.Context()))) })
	m, err := NewMetrics(mp, t.TempDir())
	require.NoError(t, err)
	return m, reader
}

func TestMetricsInvalidConfig(t *testing.T) {
	m, _ := testMetrics(t)
	config := DefaultConfig()
	config.Metrics = m
	config.WriteWatermark = 0
	s, err := New(t.Context(), config, State{}, nil)
	require.Error(t, err)
	require.Nil(t, s)
	m.mu.Lock()
	defer m.mu.Unlock()
	require.Empty(t, m.databases)
}

func collectMetrics(t *testing.T, reader *sdkmetric.ManualReader) map[string]metricdata.Aggregation {
	t.Helper()
	var data metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(t.Context(), &data))
	result := map[string]metricdata.Aggregation{}
	for _, scope := range data.ScopeMetrics {
		for _, m := range scope.Metrics {
			result[m.Name] = m.Data
		}
	}
	return result
}

func TestMetricsSizeAggregation(t *testing.T) {
	m, reader := testMetrics(t)
	a, b, unobserved := m.attach(filepath.Join(m.stateDir, "metadata_v2.db")), m.attach(filepath.Join(m.stateDir, "metadata_v2.db")), m.attach(filepath.Join(m.stateDir, "metadata_v2.db"))
	defer unobserved.close()
	require.NotContains(t, collectMetrics(t, reader), "buildkit.compaction.database.size")
	now := time.Now()
	a.observe(db.CompactionStats{Size: 1000, Reclaimable: 400}, now.Add(-10*time.Second))
	b.observe(db.CompactionStats{Size: 2000, Reclaimable: 500}, now.Add(-5*time.Second))
	// An inspection that finishes late must not overwrite a newer observation.
	a.observe(db.CompactionStats{Size: 5000}, now.Add(-20*time.Second))
	data := collectMetrics(t, reader)
	sizes := data["buildkit.compaction.database.size"].(metricdata.Gauge[int64]).DataPoints
	require.Len(t, sizes, 1)
	require.Equal(t, int64(3000), sizes[0].Value)
	kind, ok := sizes[0].Attributes.Value("database.path")
	require.True(t, ok)
	require.Equal(t, "metadata_v2.db", kind.AsString())
	require.Equal(t, int64(900), data["buildkit.compaction.database.reclaimable"].(metricdata.Gauge[int64]).DataPoints[0].Value)
	require.GreaterOrEqual(t, data["buildkit.compaction.database.observation.age"].(metricdata.Gauge[float64]).DataPoints[0].Value, float64(10))
	a.close()
	data = collectMetrics(t, reader)
	require.Equal(t, int64(2000), data["buildkit.compaction.database.size"].(metricdata.Gauge[int64]).DataPoints[0].Value)
	b.close()
	require.NotContains(t, collectMetrics(t, reader), "buildkit.compaction.database.size")
}

func TestMetricsDatabasePaths(t *testing.T) {
	m, reader := testMetrics(t)
	for _, path := range []string{"cache.db", "runc-overlayfs/metadata_v2.db", "runc-native/metadata_v2.db"} {
		d := m.attach(filepath.Join(m.stateDir, filepath.FromSlash(path)))
		defer d.close()
		require.Equal(t, path, d.path)
		d.observe(db.CompactionStats{Size: 1000}, time.Now())
	}
	data := collectMetrics(t, reader)
	require.Len(t, data["buildkit.compaction.database.size"].(metricdata.Gauge[int64]).DataPoints, 3)
	for _, path := range []string{"", filepath.Join(m.stateDir, "..", "outside.db")} {
		d := m.attach(path)
		defer d.close()
		require.Empty(t, d.path)
	}
}

func TestMetricsOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name       string
		result     db.CompactResult
		err, cause error
		reclaimed  int64
	}{
		{name: "completed", result: db.CompactResult{Compacted: true, SizeBefore: 1000, SizeAfter: 100, Duration: 2 * time.Second}, reclaimed: 900},
		{name: "skipped", result: db.CompactResult{Reason: "insufficient free disk space"}},
		{name: "canceled", result: db.CompactResult{Duration: time.Second}, err: context.Canceled, cause: errWriter},
		{name: "failed", result: db.CompactResult{Duration: time.Second}, err: errors.New("copy failed")},
		{name: "failed", result: db.CompactResult{Compacted: true, SizeBefore: 1000, SizeAfter: 100}, err: errors.New("directory sync failed"), reclaimed: 900},
		{name: "failed", result: db.CompactResult{Compacted: true, SizeBefore: 1000}, err: errors.New("stat failed")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, reader := testMetrics(t)
			d := m.attach(filepath.Join(m.stateDir, "cache.db"))
			defer d.close()
			d.record(t.Context(), true, tc.result, tc.err, tc.cause)
			data := collectMetrics(t, reader)
			points := data["buildkit.compaction.attempts"].(metricdata.Sum[int64]).DataPoints
			require.Len(t, points, 1)
			require.Equal(t, int64(1), points[0].Value)
			outcome, _ := points[0].Attributes.Value("outcome")
			require.Equal(t, tc.name, outcome.AsString())
			trigger, _ := points[0].Attributes.Value("trigger")
			require.Equal(t, "manual", trigger.AsString())
			if tc.result.Duration > 0 {
				point := data["buildkit.compaction.copy.duration"].(metricdata.Histogram[float64]).DataPoints[0]
				require.Equal(t, uint64(1), point.Count)
				require.InDelta(t, tc.result.Duration.Seconds(), point.Sum, 0.000001)
			} else {
				require.NotContains(t, data, "buildkit.compaction.copy.duration")
			}
			if tc.reclaimed > 0 {
				require.Equal(t, tc.reclaimed, data["buildkit.compaction.reclaimed"].(metricdata.Sum[int64]).DataPoints[0].Value)
			} else {
				require.NotContains(t, data, "buildkit.compaction.reclaimed")
			}
		})
	}
}

func TestMetricsCollectionDuringCopy(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m, reader := testMetrics(t)
		started := make(chan struct{})
		b := &testBackend{size: 1000, free: 500, compact: func(ctx context.Context) (db.CompactResult, error) {
			close(started)
			<-ctx.Done()
			return db.CompactResult{Duration: time.Second}, context.Cause(ctx)
		}}
		cfg := testConfig()
		cfg.Metrics, cfg.ManualOnly = m, true
		s, err := New(t.Context(), cfg, State{}, b)
		require.NoError(t, err)
		defer s.Close()
		_, err = s.Inspect()
		require.NoError(t, err)
		checks := b.checks.Load()
		s.Begin(false)
		ctx, cancel := context.WithCancelCause(t.Context())
		defer cancel(context.Canceled)
		r, err := s.Request(ctx)
		require.NoError(t, err)
		time.Sleep(2 * time.Second)
		data := collectMetrics(t, reader)
		for _, point := range data["buildkit.compaction.pending.duration"].(metricdata.Gauge[float64]).DataPoints {
			trigger, _ := point.Attributes.Value("trigger")
			if trigger.AsString() == "manual" {
				require.InDelta(t, 2, point.Value, 0.000001)
			}
		}
		s.End(false)
		<-started
		b.mu.Lock()
		data = collectMetrics(t, reader)
		b.mu.Unlock()
		require.Equal(t, checks, b.checks.Load(), "collection must not inspect the backend")
		for _, point := range data["buildkit.compaction.pending.duration"].(metricdata.Gauge[float64]).DataPoints {
			require.Zero(t, point.Value)
		}
		cancel(context.Canceled)
		require.NotEmpty(t, (<-r.Done()).Error)
		synctest.Wait()
		data = collectMetrics(t, reader)
		require.Len(t, data["buildkit.compaction.attempts"].(metricdata.Sum[int64]).DataPoints, 1)
		require.NoError(t, s.Close())
		require.NotContains(t, collectMetrics(t, reader), "buildkit.compaction.database.size")
	})
}

func TestMetricsAutomaticAttempt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m, reader := testMetrics(t)
		b := &testBackend{size: 1000, free: 500}
		b.compact = func(context.Context) (db.CompactResult, error) {
			b.mu.Lock()
			b.size, b.free = 500, 0
			b.mu.Unlock()
			return db.CompactResult{Compacted: true, SizeBefore: 1000, SizeAfter: 500, Duration: time.Second}, nil
		}
		cfg := testConfig()
		cfg.Metrics = m
		s, err := New(t.Context(), cfg, State{Writes: cfg.WriteWatermark}, b)
		require.NoError(t, err)
		defer s.Close()
		time.Sleep(cfg.IdleTimeout)
		synctest.Wait()
		data := collectMetrics(t, reader)
		point := data["buildkit.compaction.attempts"].(metricdata.Sum[int64]).DataPoints[0]
		trigger, _ := point.Attributes.Value("trigger")
		require.Equal(t, "automatic", trigger.AsString())
		require.Equal(t, int64(500), data["buildkit.compaction.database.size"].(metricdata.Gauge[int64]).DataPoints[0].Value)
		require.Zero(t, data["buildkit.compaction.database.reclaimable"].(metricdata.Gauge[int64]).DataPoints[0].Value)
	})
}

func TestMetricsPrometheus(t *testing.T) {
	registry := prometheus.NewRegistry()
	exporter, err := otelprom.New(otelprom.WithRegisterer(registry))
	require.NoError(t, err)
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(exporter))
	t.Cleanup(func() { require.NoError(t, mp.Shutdown(context.WithoutCancel(t.Context()))) })
	m, err := NewMetrics(mp, t.TempDir())
	require.NoError(t, err)
	cfg := testConfig()
	cfg.Metrics, cfg.ManualOnly = m, true
	path := filepath.Join(m.stateDir, "cache.db")
	s, err := NewFile(cfg, path, true, &testBackend{size: 1000, free: 500})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	_, err = s.Inspect()
	require.NoError(t, err)
	w := httptest.NewRecorder()
	promhttp.HandlerFor(registry, promhttp.HandlerOpts{}).ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil))
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), "buildkit_compaction_database_size_bytes")
	require.Contains(t, w.Body.String(), `database_path="cache.db"`)
	require.NotContains(t, w.Body.String(), path)
}

func TestMetricsDisabled(t *testing.T) {
	m, err := NewMetrics(nil, "")
	require.NoError(t, err)
	require.Nil(t, m)
	require.Nil(t, m.attach("cache.db"))
}
