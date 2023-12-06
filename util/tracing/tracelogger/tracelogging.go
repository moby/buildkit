// Package tracelogger provides a logger with trace- and span-ID's if tracing
// is enabled.
//
// It is separate from [github.com/moby/buildkit/util/tracing/detect] so that
// it can be imported without registering detectors.
package tracelogger

import (
	"context"

	"github.com/containerd/log"
	"go.opentelemetry.io/otel/trace"
)

var enabled bool

// Enable sets whether trace-ID logging must be enabled or disabled. It does
// not perform any synchronization, and is not suitable for concurrent use.
func Enable(v bool) {
	enabled = v
}

// Get returns a logger with trace- and span-ID's if tracing is enabled. It
// returns logger unmodified if tracing is not enabled.
func Get(ctx context.Context, parentLogger *log.Entry) *log.Entry {
	if enabled {
		if spanContext := trace.SpanFromContext(ctx).SpanContext(); spanContext.IsValid() {
			return parentLogger.WithFields(log.Fields{
				"traceID": spanContext.TraceID(),
				"spanID":  spanContext.SpanID(),
			})
		}
	}
	return parentLogger
}
