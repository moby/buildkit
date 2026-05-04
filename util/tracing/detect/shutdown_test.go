package detect_test

import (
	"context"
	"testing"
	"time"

	"github.com/moby/buildkit/util/tracing/detect"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func newTestProvider(t *testing.T) *sdktrace.TracerProvider {
	t.Helper()
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://192.0.2.1:4317") // RFC 5737 TEST-NET, guaranteed unreachable
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_PROTOCOL", "grpc")

	exp, err := detect.NewSpanExporter(context.Background())
	if err != nil {
		t.Fatalf("NewSpanExporter: %v", err)
	}
	if detect.IsNoneSpanExporter(exp) {
		t.Fatal("expected OTLP exporter, got none")
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithResource(detect.Resource()),
		sdktrace.WithBatcher(exp),
	)

	tracer := tp.Tracer("test")
	_, span := tracer.Start(context.Background(), "test-op")
	span.End()

	return tp
}

// TestShutdownStallsWithUnboundedContext reproduces
// https://github.com/moby/buildkit/issues/4616.
// Without a deadline on the context, Shutdown blocks until the BSP export
// timeout expires (default 30 s; reduced here via env for test speed).
func TestShutdownStallsWithUnboundedContext(t *testing.T) {
	t.Setenv("OTEL_BSP_EXPORT_TIMEOUT", "2000") // 2 s instead of default 30 s

	tp := newTestProvider(t)

	start := time.Now()
	_ = tp.Shutdown(context.TODO()) // the buggy call pattern
	elapsed := time.Since(start)

	// Even with the reduced timeout, shutdown still blocks for the full
	// export timeout (2 s here, 30 s in production).
	if elapsed < 1500*time.Millisecond {
		t.Fatalf("expected stall of ~2s, got %v", elapsed)
	}
	t.Logf("Unbounded shutdown stalled for %v (BSP export timeout)", elapsed)
}

// TestShutdownRespectsDeadline verifies the fix: passing a bounded context
// makes Shutdown return promptly when the deadline is shorter than the export
// timeout.
func TestShutdownRespectsDeadline(t *testing.T) {
	tp := newTestProvider(t)

	deadline := 500 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()

	start := time.Now()
	err := tp.Shutdown(ctx)
	elapsed := time.Since(start)

	if elapsed > deadline+200*time.Millisecond {
		t.Fatalf("Shutdown took %v, expected <= %v", elapsed, deadline+200*time.Millisecond)
	}
	t.Logf("Bounded shutdown completed in %v (err=%v)", elapsed, err)
}
