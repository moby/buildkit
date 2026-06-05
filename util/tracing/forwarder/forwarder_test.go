package forwarder

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

var testSpans = []sdktrace.ReadOnlySpan{nil}

type testExporter struct {
	exportSpans func(context.Context, []sdktrace.ReadOnlySpan) error
	shutdown    func(context.Context) error
}

func (e testExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	if e.exportSpans != nil {
		return e.exportSpans(ctx, spans)
	}
	return nil
}

func (e testExporter) Shutdown(ctx context.Context) error {
	if e.shutdown != nil {
		return e.shutdown(ctx)
	}
	return nil
}

func TestExportSpansDropsExpiredRequest(t *testing.T) {
	var exports atomic.Int64
	e := NewUnstarted(testExporter{
		exportSpans: func(context.Context, []sdktrace.ReadOnlySpan) error {
			exports.Add(1)
			return nil
		},
	})

	e.exportSpans(context.Background(), exportRequest{
		deadline: time.Now().Add(-time.Millisecond),
		spans:    testSpans,
	})

	require.Equal(t, int64(0), exports.Load())
}

func TestShutdownReturnsWhenExportBlocks(t *testing.T) {
	exportStarted := make(chan struct{})
	releaseExport := make(chan struct{})
	exportDone := make(chan struct{})
	var exportStartedOnce sync.Once

	e, err := New(context.Background(), testExporter{
		exportSpans: func(context.Context, []sdktrace.ReadOnlySpan) error {
			exportStartedOnce.Do(func() {
				close(exportStarted)
			})
			<-releaseExport
			close(exportDone)
			return nil
		},
	})
	require.NoError(t, err)

	require.NoError(t, e.ExportSpans(context.Background(), testSpans))
	requireCloses(t, exportStarted)

	ctx, cancel := context.WithTimeoutCause(context.Background(), 50*time.Millisecond, context.DeadlineExceeded)
	defer cancel()

	start := time.Now()
	err = e.Shutdown(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(start), time.Second)

	close(releaseExport)
	requireCloses(t, exportDone)
	requireCloses(t, e.done)
}

func TestShutdownPassesContextToExporterShutdown(t *testing.T) {
	shutdownStarted := make(chan struct{})

	e, err := New(context.Background(), testExporter{
		shutdown: func(ctx context.Context) error {
			close(shutdownStarted)
			<-ctx.Done()
			return context.Cause(ctx)
		},
	})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeoutCause(context.Background(), 50*time.Millisecond, context.DeadlineExceeded)
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- e.Shutdown(ctx)
	}()

	requireCloses(t, shutdownStarted)

	select {
	case err := <-errCh:
		require.ErrorIs(t, err, context.DeadlineExceeded)
	case <-time.After(time.Second):
		t.Fatal("shutdown did not return after the context deadline")
	}
}

func TestShutdownDrainsPendingExports(t *testing.T) {
	var exports atomic.Int64
	var shutdowns atomic.Int64
	e, err := New(context.Background(), testExporter{
		exportSpans: func(context.Context, []sdktrace.ReadOnlySpan) error {
			exports.Add(1)
			return nil
		},
		shutdown: func(context.Context) error {
			shutdowns.Add(1)
			return nil
		},
	})
	require.NoError(t, err)

	require.NoError(t, e.ExportSpans(context.Background(), testSpans))
	require.NoError(t, e.ExportSpans(context.Background(), testSpans))
	require.NoError(t, e.ExportSpans(context.Background(), testSpans))
	require.NoError(t, e.Shutdown(context.Background()))

	require.Equal(t, int64(3), exports.Load())
	require.Equal(t, int64(1), shutdowns.Load())
}

func requireCloses(t *testing.T, ch <-chan struct{}) {
	t.Helper()

	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("channel did not close")
	}
}
