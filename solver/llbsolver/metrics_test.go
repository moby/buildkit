package llbsolver

import (
	"context"
	"testing"
	"time"

	controlapi "github.com/moby/buildkit/api/services/control"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	rpcstatus "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// newTestMetrics returns a buildMetrics wired to a ManualReader so that
// tests can collect and inspect emitted observations synchronously.
func newTestMetrics(t *testing.T) (*buildMetrics, *sdkmetric.ManualReader) {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	bm, err := newBuildMetrics(mp)
	require.NoError(t, err)
	return bm, reader
}

// collect collects all metrics from the reader into a flat map keyed by
// instrument name. Each value is the metricdata.Aggregation (Sum,
// Histogram, etc.) for that instrument.
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

// findCounterPoint returns the int64 sum data point whose attributes
// contain every (key, value) pair in want. Fails the test if no
// matching point is found.
func findCounterPoint(t *testing.T, agg metricdata.Aggregation, want map[string]string) metricdata.DataPoint[int64] {
	t.Helper()
	sum, ok := agg.(metricdata.Sum[int64])
	require.True(t, ok, "expected Sum[int64], got %T", agg)
	for _, dp := range sum.DataPoints {
		if attrsContain(dp.Attributes, want) {
			return dp
		}
	}
	t.Fatalf("no data point with attrs %v found among %d points", want, len(sum.DataPoints))
	return metricdata.DataPoint[int64]{}
}

// attrsContain reports whether every (key, value) pair in want is
// present in set.
func attrsContain(set attribute.Set, want map[string]string) bool {
	for k, v := range want {
		got, ok := set.Value(attribute.Key(k))
		if !ok || got.AsString() != v {
			return false
		}
	}
	return true
}

func TestRecordBuildCompletion_Success(t *testing.T) {
	bm, reader := newTestMetrics(t)

	start := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	rec := &controlapi.BuildHistoryRecord{
		CreatedAt:         timestamppb.New(start),
		CompletedAt:       timestamppb.New(start.Add(7 * time.Second)),
		NumCompletedSteps: 5,
		NumCachedSteps:    2,
		NumTotalSteps:     5,
		NumWarnings:       1,
	}

	bm.recordBuildCompletion(context.Background(), rec)
	got := collect(t, reader)

	builds := findCounterPoint(t, got["buildkit.builds"], map[string]string{
		"status": "success",
	})
	require.Equal(t, int64(1), builds.Value)
	// On success, error_code must not be present at all.
	if _, ok := builds.Attributes.Value("error_code"); ok {
		t.Fatal("error_code attribute should be absent on success")
	}

	hist, ok := got["buildkit.build.duration"].(metricdata.Histogram[float64])
	require.True(t, ok, "expected Histogram[float64] for build.duration, got %T", got["buildkit.build.duration"])
	require.Len(t, hist.DataPoints, 1)
	require.Equal(t, uint64(1), hist.DataPoints[0].Count)
	require.InDelta(t, 7.0, hist.DataPoints[0].Sum, 0.001)
	require.True(t, attrsContain(hist.DataPoints[0].Attributes, map[string]string{
		"status": "success",
	}))
}

func TestRecordBuildCompletion_FailureGRPCCodes(t *testing.T) {
	cases := []struct {
		name string
		code codes.Code
	}{
		{"canceled", codes.Canceled},
		{"resource_exhausted", codes.ResourceExhausted},
		{"unknown", codes.Unknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bm, reader := newTestMetrics(t)
			start := time.Now()
			rec := &controlapi.BuildHistoryRecord{
				CreatedAt:   timestamppb.New(start),
				CompletedAt: timestamppb.New(start.Add(time.Second)),
				Error:       &rpcstatus.Status{Code: int32(tc.code), Message: "ignored: must not appear as a label"},
			}
			bm.recordBuildCompletion(context.Background(), rec)

			builds := findCounterPoint(t, collect(t, reader)["buildkit.builds"], map[string]string{
				"status":     "failure",
				"error_code": tc.code.String(),
			})
			require.Equal(t, int64(1), builds.Value)
		})
	}
}

func TestRecordBuildCompletion_StepCounters(t *testing.T) {
	bm, reader := newTestMetrics(t)
	start := time.Now()
	rec := &controlapi.BuildHistoryRecord{
		CreatedAt:         timestamppb.New(start),
		CompletedAt:       timestamppb.New(start.Add(time.Second)),
		NumCompletedSteps: 8,
		NumCachedSteps:    3,
		NumTotalSteps:     10,
		NumWarnings:       2,
	}
	bm.recordBuildCompletion(context.Background(), rec)

	steps := collect(t, reader)["buildkit.builds.steps"]
	for kind, want := range map[string]int64{
		"completed": 8,
		"cached":    3,
		"total":     10,
		"warnings":  2,
	} {
		dp := findCounterPoint(t, steps, map[string]string{"kind": kind})
		require.Equal(t, want, dp.Value, "kind=%s", kind)
	}
}

func TestRecordBuildCompletion_NilSafe(t *testing.T) {
	// Nil receiver and nil record must both be no-ops so that callers
	// at solver/llbsolver/history.go do not have to guard the call.
	var bm *buildMetrics
	require.NotPanics(t, func() {
		bm.recordBuildCompletion(context.Background(), &controlapi.BuildHistoryRecord{})
	})

	bm, _ = newTestMetrics(t)
	require.NotPanics(t, func() {
		bm.recordBuildCompletion(context.Background(), nil)
	})
}

func TestNewBuildMetrics_NilProviderUsesNoop(t *testing.T) {
	// Passing a nil MeterProvider must succeed and produce a usable
	// metrics struct. This keeps tests and alternative integrations
	// from having to wire OTEL just to construct the solver.
	bm, err := newBuildMetrics(nil)
	require.NoError(t, err)
	require.NotNil(t, bm)
	require.NotPanics(t, func() {
		bm.recordBuildCompletion(context.Background(), &controlapi.BuildHistoryRecord{
			CreatedAt:   timestamppb.Now(),
			CompletedAt: timestamppb.Now(),
		})
	})
}
