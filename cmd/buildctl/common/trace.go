package common

import (
	"context"
	"os"
	"time"

	"github.com/moby/buildkit/util/appcontext"
	"github.com/moby/buildkit/util/tracing/delegated"
	"github.com/moby/buildkit/util/tracing/detect"
	"github.com/pkg/errors"
	"github.com/urfave/cli"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

const exportTimeout = 50 * time.Millisecond

func AttachAppContext(app *cli.App) error {
	ctx := appcontext.Context()

	exp, err := detect.NewSpanExporter(ctx)
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
			app.Commands[i].Before = func(clicontext *cli.Context) error {
				if before != nil {
					if err := before(clicontext); err != nil {
						return err
					}
				}

				ctx := ctx
				ctx, span = tracer.Start(ctx, name, trace.WithAttributes(
					attribute.StringSlice("command", os.Args),
				))

				clicontext.App.Metadata["context"] = ctx
				return nil
			}
		}(cmd.Before)
	}

	app.ExitErrHandler = func(clicontext *cli.Context, err error) {
		if span != nil && err != nil {
			span.SetStatus(codes.Error, err.Error())
		}
		cli.HandleExitCoder(err)
	}

	after := app.After
	app.After = func(clicontext *cli.Context) error {
		if after != nil {
			if err := after(clicontext); err != nil {
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

		return tp.Shutdown(ctx)
	}
	return nil
}

func CommandContext(c *cli.Context) context.Context {
	return c.App.Metadata["context"].(context.Context)
}
