package common

import (
	"context"
	"os"
	"time"

	"github.com/moby/buildkit/util/appcontext"
	"github.com/moby/buildkit/util/tracing/delegated"
	"github.com/moby/buildkit/util/tracing/detect"
	"github.com/pkg/errors"
	"github.com/urfave/cli/v3"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

const exportTimeout = 50 * time.Millisecond

func AttachAppContext(app *cli.Command) error {
	baseCtx := appcontext.Context()

	exp, err := detect.NewSpanExporter(baseCtx)
	if err != nil {
		return err
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithResource(detect.Resource()),
		sdktrace.WithBatcher(exp),
		sdktrace.WithBatcher(delegated.DefaultExporter),
	)
	tracer := tp.Tracer("")

	var span trace.Span

	for i, cmd := range app.Commands {
		func(before cli.BeforeFunc) {
			name := cmd.Name
			app.Commands[i].Before = func(_ context.Context, clicontext *cli.Command) (context.Context, error) {
				cmdCtx := baseCtx
				if before != nil {
					var err error
					cmdCtx, err = before(cmdCtx, clicontext)
					if err != nil {
						return cmdCtx, err
					}
					if cmdCtx == nil {
						cmdCtx = baseCtx
					}
				}

				cmdCtx, span = tracer.Start(cmdCtx, name, trace.WithAttributes(
					attribute.StringSlice("command", os.Args),
				))

				clicontext.Root().Metadata["context"] = cmdCtx
				return cmdCtx, nil
			}
		}(cmd.Before)
	}

	app.ExitErrHandler = func(_ context.Context, _ *cli.Command, err error) {
		if span != nil && err != nil {
			span.SetStatus(codes.Error, err.Error())
		}
		cli.HandleExitCoder(err)
	}

	after := app.After
	app.After = func(ctx context.Context, clicontext *cli.Command) error {
		if after != nil {
			if err := after(ctx, clicontext); err != nil {
				return err
			}
		}
		if span != nil {
			span.End()
		}

		// Set a rather aggressive timeout for shutting down the tracer provider
		// to ensure we don't stall on a non-responsive tracing endpoint for too long
		// on shutdown.
		ctx, cancel := context.WithTimeoutCause(appcontext.Shutdown(), exportTimeout, errors.WithStack(context.DeadlineExceeded))
		defer cancel()

		if err := tp.Shutdown(ctx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return nil
	}
	return nil
}

func CommandContext(c *cli.Command) context.Context {
	return c.Root().Metadata["context"].(context.Context)
}
