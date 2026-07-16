package common

import (
	"context"
	"os"

	"github.com/moby/buildkit/util/appcontext"
	"github.com/moby/buildkit/util/tracing/delegated"
	"github.com/moby/buildkit/util/tracing/detect"
	"github.com/urfave/cli/v3"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

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

		return tp.Shutdown(context.TODO())
	}
	return nil
}

func CommandContext(c *cli.Command) context.Context {
	return c.Root().Metadata["context"].(context.Context)
}
