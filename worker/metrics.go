package worker

import (
	"context"

	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
)

// instrumentationName is the OTEL instrumentation scope for the worker
// observability instruments.
const instrumentationName = "github.com/moby/buildkit/worker"

// RegisterMetrics registers the worker observability instruments against mp.
// A nil mp disables metrics via a no-op provider so callers need not guard the
// disabled path; a registration error fails daemon startup fast. It must be
// called after all workers have been added to c.
//
// buildkit.workers reports the worker count at collection time. The worker set
// is populated at startup and not mutated while serving, so the callback needs
// no lock.
func RegisterMetrics(mp metric.MeterProvider, c *Controller) error {
	if mp == nil {
		mp = noop.NewMeterProvider()
	}
	meter := mp.Meter(instrumentationName)

	_, err := meter.Int64ObservableGauge(
		"buildkit.workers",
		metric.WithDescription("Number of workers currently registered with the daemon."),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(int64(len(c.workers)))
			return nil
		}),
	)
	return err
}
