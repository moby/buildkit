package tracing

import (
	"context"
	"sync"

	"github.com/pkg/errors"
	"go.opentelemetry.io/otel/sdk/trace"
)

const maxBuffer = 256

type DelegatedSpanExporter struct {
	mu       sync.Mutex
	delegate trace.SpanExporter
	buffer   []trace.ReadOnlySpan
}

func NewDelegatedSpanExporter() *DelegatedSpanExporter {
	return &DelegatedSpanExporter{}
}

func (e *DelegatedSpanExporter) ExportSpans(ctx context.Context, spans []trace.ReadOnlySpan) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.delegate == nil {
		var err error
		if remaining := maxBuffer - len(e.buffer); remaining < len(spans) {
			spans = spans[:remaining]
			err = errors.New("partial write")
		}
		e.buffer = append(e.buffer, spans...)
		return err
	}

	if len(e.buffer) > 0 {
		if err := e.delegate.ExportSpans(ctx, e.buffer); err != nil {
			return err
		}
		e.buffer = nil
	}
	return e.delegate.ExportSpans(ctx, spans)
}

func (e *DelegatedSpanExporter) Shutdown(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.delegate == nil {
		e.buffer = nil
		return nil
	}

	if len(e.buffer) > 0 {
		if err := e.delegate.ExportSpans(ctx, e.buffer); err != nil {
			return err
		}
	}
	return e.delegate.Shutdown(ctx)
}

func (e *DelegatedSpanExporter) SetSpanExporter(exporter trace.SpanExporter) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.delegate != nil {
		return errors.New("already initialized")
	}

	e.delegate = exporter
	return nil
}
