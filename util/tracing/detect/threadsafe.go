package detect

import (
	"context"
	"sync"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// Duplicated from buildkit/client.
// Part of the external interface of our tracer.
// Needed to ensure that we maintain this interface.
type tracerDelegate interface {
	sdktrace.SpanExporter

	SetSpanExporter(ctx context.Context, exp sdktrace.SpanExporter) error
}

func makeThreadSafe(exp sdktrace.SpanExporter) sdktrace.SpanExporter {
	d, isDelegate := exp.(tracerDelegate)
	if isDelegate {
		return &threadSafeDelegateWrapper{
			exporter: d,
		}
	}
	return &threadSafeExporterWrapper{
		exporter: exp,
	}
}

// threadSafeExporterWrapper wraps an OpenTelemetry SpanExporter and makes it thread-safe.
type threadSafeExporterWrapper struct {
	mu       sync.Mutex
	exporter sdktrace.SpanExporter
}

func (tse *threadSafeExporterWrapper) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	tse.mu.Lock()
	defer tse.mu.Unlock()
	return tse.exporter.ExportSpans(ctx, spans)
}

func (tse *threadSafeExporterWrapper) Shutdown(ctx context.Context) error {
	tse.mu.Lock()
	defer tse.mu.Unlock()
	return tse.exporter.Shutdown(ctx)
}

type threadSafeDelegateWrapper struct {
	mu       sync.Mutex
	exporter tracerDelegate
}

func (tse *threadSafeDelegateWrapper) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	tse.mu.Lock()
	defer tse.mu.Unlock()
	return tse.exporter.ExportSpans(ctx, spans)
}

func (tse *threadSafeDelegateWrapper) Shutdown(ctx context.Context) error {
	tse.mu.Lock()
	defer tse.mu.Unlock()
	return tse.exporter.Shutdown(ctx)
}

func (tse *threadSafeDelegateWrapper) SetSpanExporter(ctx context.Context, exp sdktrace.SpanExporter) error {
	tse.mu.Lock()
	defer tse.mu.Unlock()
	return tse.exporter.SetSpanExporter(ctx, exp)
}
