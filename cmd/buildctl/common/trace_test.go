package common

import (
	"context"
	"errors"
	"testing"

	"github.com/moby/buildkit/util/tracing/detect"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v3"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

var errTraceShutdown = errors.New("trace shutdown failed")

func TestTraceShutdownDeadline(t *testing.T) {
	withTraceExporter(t, "deadline", shutdownDeadlineExporter{})

	app := traceTestApp(t, nil)
	require.NoError(t, app.Run(context.Background(), []string{"buildctl", "noop"}))
}

func TestTraceShutdownError(t *testing.T) {
	withTraceExporter(t, "error", shutdownErrorExporter{})

	app := traceTestApp(t, nil)
	require.ErrorIs(t, app.Run(context.Background(), []string{"buildctl", "noop"}), errTraceShutdown)
}

func TestTraceAfterError(t *testing.T) {
	withTraceExporter(t, "noop", shutdownErrorExporter{})

	errAfter := errors.New("after failed")
	app := traceTestApp(t, func(context.Context, *cli.Command) error {
		return errAfter
	})
	require.ErrorIs(t, app.Run(context.Background(), []string{"buildctl", "noop"}), errAfter)
}

func withTraceExporter(t *testing.T, name string, exp sdktrace.SpanExporter) {
	t.Helper()

	exporterName := "buildctl-common-test-" + name
	detect.Register(exporterName, detect.TraceExporterDetector(func() (sdktrace.SpanExporter, error) {
		return exp, nil
	}), 0)
	t.Setenv("OTEL_TRACES_EXPORTER", exporterName)
}

func traceTestApp(t *testing.T, after cli.AfterFunc) *cli.Command {
	t.Helper()

	app := &cli.Command{
		Name:  "buildctl",
		After: after,
		Commands: []*cli.Command{
			{
				Name: "noop",
				Action: func(context.Context, *cli.Command) error {
					return nil
				},
			},
		},
	}
	require.NoError(t, AttachAppContext(app))
	return app
}

type shutdownDeadlineExporter struct{}

func (shutdownDeadlineExporter) ExportSpans(context.Context, []sdktrace.ReadOnlySpan) error {
	return nil
}

func (shutdownDeadlineExporter) Shutdown(ctx context.Context) error {
	<-ctx.Done()
	return context.Cause(ctx)
}

type shutdownErrorExporter struct{}

func (shutdownErrorExporter) ExportSpans(context.Context, []sdktrace.ReadOnlySpan) error {
	return nil
}

func (shutdownErrorExporter) Shutdown(context.Context) error {
	return errTraceShutdown
}
