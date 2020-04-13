package process

import (
	"context"
	"errors"
	"github.com/opentracing/opentracing-go"
	"go.undefinedlabs.com/scopeagent/instrumentation"
	"go.undefinedlabs.com/scopeagent/tracer"
	"os"
	"sync"
)

var (
	processSpanContext opentracing.SpanContext
	once               sync.Once
)

// Injects a context to the environment variables array
func InjectFromContext(ctx context.Context, env *[]string) error {
	if span := opentracing.SpanFromContext(ctx); span != nil {
		return Inject(span.Context(), env)
	}
	return errors.New("there are no spans in the context")
}

// Injects the span context to the environment variables array
func Inject(sm opentracing.SpanContext, env *[]string) error {
	return instrumentation.Tracer().Inject(sm, tracer.EnvironmentVariableFormat, env)
}

// Extracts the span context from an environment variables array
func Extract(env *[]string) (opentracing.SpanContext, error) {
	return instrumentation.Tracer().Extract(tracer.EnvironmentVariableFormat, env)
}

// Gets the current span context from the environment variables, if available
func SpanContext() opentracing.SpanContext {
	once.Do(func() {
		env := os.Environ()
		if envCtx, err := Extract(&env); err == nil {
			processSpanContext = envCtx
		}
	})
	return processSpanContext
}

func StartSpan(opts ...opentracing.StartSpanOption) opentracing.Span {
	if spanCtx := SpanContext(); spanCtx != nil {
		opts = append(opts, opentracing.ChildOf(spanCtx))
	}
	return instrumentation.Tracer().StartSpan(getOperationNameFromArgs(os.Args), opts...)
}

func startSpanWithOperationName(operationName string, opts ...opentracing.StartSpanOption) opentracing.Span {
	if spanCtx := SpanContext(); spanCtx != nil {
		opts = append(opts, opentracing.ChildOf(spanCtx))
	}
	return instrumentation.Tracer().StartSpan(operationName, opts...)
}

func StartSpanFromContext(ctx context.Context, operationName string, opts ...opentracing.StartSpanOption) (opentracing.Span, context.Context) {
	if parentSpan := opentracing.SpanFromContext(ctx); parentSpan != nil {
		opts = append(opts, opentracing.ChildOf(parentSpan.Context()))
	}
	span := startSpanWithOperationName(operationName, opts...)
	return span, opentracing.ContextWithSpan(ctx, span)
}
