package worker

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// stubWorker satisfies Worker for count-only tests; RegisterMetrics never
// calls into a worker.
type stubWorker struct {
	Worker
}

func TestRegisterMetricsWorkerCount(t *testing.T) {
	for _, tc := range []struct {
		name string
		n    int
	}{
		{name: "no workers", n: 0},
		{name: "single worker", n: 1},
		{name: "multiple workers", n: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &Controller{}
			for range tc.n {
				require.NoError(t, c.Add(stubWorker{}))
			}

			reader := sdkmetric.NewManualReader()
			mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
			require.NoError(t, RegisterMetrics(mp, c))

			gauge, ok := collect(t, reader)["buildkit.workers"].(metricdata.Gauge[int64])
			require.True(t, ok, "buildkit.workers must be an int64 gauge")
			require.Len(t, gauge.DataPoints, 1)
			require.Equal(t, int64(tc.n), gauge.DataPoints[0].Value)
		})
	}
}

func TestRegisterMetricsNilProvider(t *testing.T) {
	require.NotPanics(t, func() {
		require.NoError(t, RegisterMetrics(nil, &Controller{}))
	})
}

// collect flattens all instruments from the reader into a map keyed by
// instrument name.
func collect(t *testing.T, reader *sdkmetric.ManualReader) map[string]metricdata.Aggregation {
	t.Helper()
	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))
	out := map[string]metricdata.Aggregation{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			out[m.Name] = m.Data
		}
	}
	return out
}
