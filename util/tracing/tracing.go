package tracing

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.4.0"
	"go.opentelemetry.io/otel/trace"
)

// StartSpan starts a new span as a child of the span in context.
// If there is no span in context then this is a no-op.
func StartSpan(ctx context.Context, operationName string, opts ...trace.SpanStartOption) (trace.Span, context.Context) {
	parent := trace.SpanFromContext(ctx)
	tracer := trace.NewNoopTracerProvider().Tracer("")
	if parent.SpanContext().IsValid() {
		tracer = parent.TracerProvider().Tracer("")
	}
	ctx, span := tracer.Start(ctx, operationName, opts...)
	return span, ctx
}

// FinishWithError finalizes the span and sets the error if one is passed
func FinishWithError(span trace.Span, err error) {
	if err != nil {
		span.RecordError(err)
		if _, ok := err.(interface {
			Cause() error
		}); ok {
			span.SetAttributes(attribute.String(string(semconv.ExceptionStacktraceKey), fmt.Sprintf("%+v", err)))
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
	if span := trace.SpanFromContext(ctx2); span != nil {
		return trace.ContextWithSpan(ctx, span)
	}
	return ctx
}

var DefaultTransport http.RoundTripper = &Transport{
	RoundTripper: NewTransport(http.DefaultTransport),
}

var DefaultClient = &http.Client{
	Transport: DefaultTransport,
}

var propagators = propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{})

type Transport struct {
	http.RoundTripper
}

func NewTransport(rt http.RoundTripper) http.RoundTripper {
	// TODO: switch to otelhttp. needs upstream updates to avoid transport-global tracer
	return &Transport{
		RoundTripper: rt,
	}
}

func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	span := trace.SpanFromContext(req.Context())
	if !span.SpanContext().IsValid() { // no tracer connected with either request or transport
		return t.RoundTripper.RoundTrip(req)
	}

	ctx, span := span.TracerProvider().Tracer("").Start(req.Context(), req.Method)

	req = req.WithContext(ctx)
	span.SetAttributes(semconv.HTTPClientAttributesFromHTTPRequest(req)...)
	propagators.Inject(ctx, propagation.HeaderCarrier(req.Header))

	resp, err := t.RoundTripper.RoundTrip(req)
	if err != nil {
		span.RecordError(err)
		span.End()
		return resp, err
	}

	span.SetAttributes(semconv.HTTPAttributesFromHTTPStatusCode(resp.StatusCode)...)
	span.SetStatus(semconv.SpanStatusFromHTTPStatusCode(resp.StatusCode))

	if req.Method == "HEAD" {
		span.End()
	} else {
		resp.Body = &wrappedBody{ctx: ctx, span: span, body: resp.Body}
	}

	return resp, err
}

type wrappedBody struct {
	ctx  context.Context
	span trace.Span
	body io.ReadCloser
}

var _ io.ReadCloser = &wrappedBody{}

func (wb *wrappedBody) Read(b []byte) (int, error) {
	n, err := wb.body.Read(b)

	switch err {
	case nil:
		// nothing to do here but fall through to the return
	case io.EOF:
		wb.span.End()
	default:
		wb.span.RecordError(err)
	}
	return n, err
}

func (wb *wrappedBody) Close() error {
	wb.span.End()
	return wb.body.Close()
}
