package tracing

import (
	"context"
	stderrors "errors"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

type MultiSpanExporter []sdktrace.SpanExporter

func (m MultiSpanExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	var errs []error
	for _, exp := range m {
		errs = append(errs, exp.ExportSpans(ctx, spans))
	}
	return stderrors.Join(errs...)
}

func (m MultiSpanExporter) Shutdown(ctx context.Context) error {
	var errs []error
	for _, exp := range m {
		errs = append(errs, exp.Shutdown(ctx))
	}
	return stderrors.Join(errs...)
}
