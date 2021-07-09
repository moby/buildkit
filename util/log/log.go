package log

import (
	"context"

	"github.com/containerd/containerd/log"
	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel/trace"
)

func init() {
	log.G = GetLogger
}

func GetLogger(ctx context.Context) *logrus.Entry {
	l := log.GetLogger(ctx)

	spanContext := trace.SpanFromContext(ctx).SpanContext()

	if spanContext.IsValid() {
		return l.WithFields(logrus.Fields{
			"traceID": spanContext.TraceID(),
			"spanID":  spanContext.SpanID(),
		})
	}

	return l
}
