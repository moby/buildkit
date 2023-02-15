package childprocess

import (
	"context"
	"os"

	"go.opentelemetry.io/otel/propagation"
)

const (
	// go.opentelemetry.io/otel/propagation doesn't export these as constats
	traceparentHeader = "traceparent"
	tracestateHeader  = "tracestate"
)

// this code can be simplified once open-telemetry/opentelemetry-specification#740 is closed and implemented in SDK

var supportedKeys = []struct{ parent, state string }{
	{parent: "TRACEPARENT", state: "TRACESTATE"},
	{parent: "OTEL_TRACE_PARENT", state: "OTEL_TRACE_STATE"},
}

func ContextFromEnv(ctx context.Context) context.Context {
	for _, keys := range supportedKeys {
		parent := os.Getenv(keys.parent)
		if parent != "" {
			mapCarrier := propagation.MapCarrier{
				traceparentHeader: parent,
				tracestateHeader:  os.Getenv(keys.state),
			}
			return (propagation.TraceContext{}).Extract(ctx, mapCarrier)
		}
	}
	return ctx
}

func MakeEnvFromContext(ctx context.Context) []string {
	mapCarrier := propagation.MapCarrier{}
	(propagation.TraceContext{}).Inject(ctx, mapCarrier)

	var env []string

	parent := mapCarrier.Get(traceparentHeader)
	state := mapCarrier.Get(tracestateHeader)
	for _, keys := range supportedKeys {
		if parent != "" {
			env = append(env, keys.parent+"="+parent)
		}
		if state != "" {
			env = append(env, keys.state+"="+state)
		}
	}
	return env
}
