package forwarder

import (
	"context"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

type Exporter struct {
	bsp sdktrace.SpanProcessor
}

func NewExporter(exp sdktrace.SpanExporter) *Exporter {
	return &Exporter{
		bsp: sdktrace.NewBatchSpanProcessor(exp),
	}
}

func (e *Exporter) ExportSpans(ctx context.Context, ss []sdktrace.ReadOnlySpan) error {
	for _, s := range ss {
		e.bsp.OnEnd(s)
	}
	return nil
}

func (e *Exporter) Shutdown(ctx context.Context) error {
	return e.bsp.Shutdown(ctx)
}
