package common

import (
	"context"
	"os"

	"github.com/moby/buildkit/util/appcontext"
	"github.com/moby/buildkit/util/tracing"
	"github.com/moby/buildkit/util/tracing/detect"
	"github.com/pkg/errors"
	"github.com/urfave/cli"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

func AttachAppContext(app *cli.App) error {
	ctx := appcontext.Context()

	delegate := tracing.NewDelegatedSpanExporter()
	topts := []sdktrace.TracerProviderOption{
		sdktrace.WithResource(detect.Resource()),
		sdktrace.WithBatcher(delegate),
	}

	exporter, _, err := detect.Exporter()
	if err != nil {
		return err
	} else if exporter != nil {
		topts = append(topts, sdktrace.WithBatcher(exporter))
	}

	tp := sdktrace.NewTracerProvider(topts...)
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

				ctx, span = tracer.Start(ctx, name, trace.WithAttributes(
					attribute.StringSlice("command", os.Args),
				))

				clicontext.App.Metadata["context"] = ctx
				clicontext.App.Metadata[delegatedSpanExporter] = delegate
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
		return tp.Shutdown(context.TODO())
	}
	return nil
}

func CommandContext(c *cli.Context) context.Context {
	return c.App.Metadata["context"].(context.Context)
}

const delegatedSpanExporter = "delegated-span-exporter"

func SetSpanExporter(c *cli.Context, init func() (sdktrace.SpanExporter, error)) error {
	md := c.App.Metadata[delegatedSpanExporter]
	if md == nil {
		return nil
	}

	delegate, ok := md.(*tracing.DelegatedSpanExporter)
	if !ok {
		return errors.New("invalid delegated span exporter")
	}

	exporter, err := init()
	if err != nil {
		return err
	}
	return delegate.SetSpanExporter(exporter)
}
