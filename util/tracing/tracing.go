package tracing

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptrace"
	"runtime"
	"strings"
	"sync"

	"github.com/moby/buildkit/util/bklog"
	"github.com/moby/buildkit/util/stack"
	"github.com/pkg/errors"
	"go.opentelemetry.io/contrib/instrumentation/net/http/httptrace/otelhttptrace"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

var noopTracer = sync.OnceValue(func() trace.TracerProvider {
	return noop.NewTracerProvider()
})

// Tracer is a utility function for creating a tracer. It will create
// a tracer with the name matching the package of the caller with the given options.
//
// This function isn't meant to be called in a tight loop. It is intended to be
// called on initialization and the tracer is supposed to be retained for future use.
func Tracer(tp trace.TracerProvider, options ...trace.TracerOption) trace.Tracer {
	if tp == nil {
		// Potentially consider using otel.GetTracerProvider here, but
		// we know of some issues where the default tracer provider can cause
		// memory leaks if it is never initialized so just being cautious
		// and using the noop tracer because buildkit itself doesn't use the global
		// tracer provider.
		return noopTracer().Tracer("", options...)
	}

	var (
		name    string
		callers [1]uintptr
	)

	if runtime.Callers(2, callers[:]) > 0 {
		frames := runtime.CallersFrames(callers[:])
		frame, _ := frames.Next()

		if frame.Function != "" {
			lastSlash := strings.LastIndex(frame.Function, "/")
			if lastSlash < 0 {
				lastSlash = 0
			}

			if funcStart := strings.Index(frame.Function[lastSlash:], "."); funcStart >= 0 {
				name = frame.Function[:lastSlash+funcStart]
			} else {
				name = frame.Function
			}
		}
	}
	return tp.Tracer(name, options...)
}

// StartSpan starts a new span as a child of the span in context.
// If there is no span in context then this is a no-op.
func StartSpan(ctx context.Context, operationName string, opts ...trace.SpanStartOption) (trace.Span, context.Context) {
	parent := trace.SpanFromContext(ctx)
	tracer := noopTracer().Tracer("")
	if parent != nil && parent.SpanContext().IsValid() {
		tracer = parent.TracerProvider().Tracer("")
	}
	ctx, span := tracer.Start(ctx, operationName, opts...)
	ctx = bklog.WithLogger(ctx, bklog.GetLogger(ctx).WithField("span", operationName))
	return span, ctx
}

func hasStacktrace(err error) bool {
	switch e := err.(type) {
	case interface{ StackTrace() *stack.Stack }:
		return true
	case interface{ StackTrace() errors.StackTrace }:
		return true
	case interface{ Unwrap() error }:
		return hasStacktrace(e.Unwrap())
	case interface{ Unwrap() []error }:
		for _, ue := range e.Unwrap() {
			if hasStacktrace(ue) {
				return true
			}
		}
	}
	return false
}

// FinishWithError finalizes the span and sets the error if one is passed
func FinishWithError(span trace.Span, err error) {
	if err != nil {
		span.RecordError(err)
		if hasStacktrace(err) {
			span.SetAttributes(attribute.String(string(semconv.ExceptionStacktraceKey), fmt.Sprintf("%+v", stack.Formatter(err))))
		}
		span.SetStatus(codes.Error, err.Error())
	}
	span.End()
}

// ContextWithSpanFromContext sets the tracing span of a context from other
// context if one is not already set. Alternative would be
// context.WithoutCancel() that would copy the context but reset ctx.Done
func ContextWithSpanFromContext(ctx, ctx2 context.Context) context.Context {
	// if already is a span then noop
	if span := trace.SpanFromContext(ctx); span.SpanContext().IsValid() {
		return ctx
	}
	if span := trace.SpanFromContext(ctx2); span != nil && span.SpanContext().IsValid() {
		return trace.ContextWithSpan(ctx, span)
	}
	return ctx
}

var DefaultTransport = NewTransport(http.DefaultTransport)

var DefaultClient = &http.Client{
	Transport: DefaultTransport,
}

func NewTransport(rt http.RoundTripper) http.RoundTripper {
	return otelhttp.NewTransport(rt,
		otelhttp.WithPropagators(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{})),
		otelhttp.WithClientTrace(func(ctx context.Context) *httptrace.ClientTrace {
			return otelhttptrace.NewClientTrace(ctx, otelhttptrace.WithoutSubSpans())
		}),
	)
}
