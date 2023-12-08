package cache

import (
	"context"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

const (
	instrumentationName      = "github.com/moby/buildkit/cache"
	metricCacheRecords       = "cache.records.count"
	metricCachePruneDuration = "cache.prune.duration"
)

type metrics struct {
	CacheRecords       metric.Int64ObservableGauge
	CachePruneDuration metric.Int64Histogram
	meter              metric.Meter
	regs               []metric.Registration
}

func newMetrics(cm *cacheManager, mp metric.MeterProvider) *metrics {
	m := &metrics{}

	var err error
	m.meter = mp.Meter(instrumentationName)

	m.CacheRecords, err = m.meter.Int64ObservableGauge(metricCacheRecords,
		metric.WithDescription("Number of cache records."),
	)
	if err != nil {
		otel.Handle(err)
	}

	m.CachePruneDuration, err = m.meter.Int64Histogram(metricCachePruneDuration,
		metric.WithDescription("Measures the duration of cache prune operations."),
		metric.WithUnit("ms"),
	)
	if err != nil {
		otel.Handle(err)
	}

	reg, err := m.meter.RegisterCallback(cm.collectMetrics, m.CacheRecords)
	if err != nil {
		otel.Handle(err)
	}
	m.regs = append(m.regs, reg)

	return m
}

func (m *metrics) MeasurePrune() (record func(ctx context.Context)) {
	start := time.Now()
	return func(ctx context.Context) {
		dur := int64(time.Since(start) / time.Millisecond)
		m.CachePruneDuration.Record(ctx, dur)
	}
}

func (m *metrics) Close() error {
	for _, reg := range m.regs {
		_ = reg.Unregister()
	}
	return nil
}

type cacheStats struct {
	NumRecords int64
}

func (cm *cacheManager) readStats() (stats cacheStats) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	stats.NumRecords = int64(len(cm.records))
	return stats
}

func (cm *cacheManager) collectMetrics(ctx context.Context, o metric.Observer) error {
	stats := cm.readStats()
	o.ObserveInt64(cm.metrics.CacheRecords, stats.NumRecords)
	return nil
}
