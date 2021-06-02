package common

import (
	"context"
	"os"
	"strings"

	"github.com/moby/buildkit/util/appcontext"
	"github.com/moby/buildkit/util/tracing/detect"
	_ "github.com/moby/buildkit/util/tracing/detect/jaeger"
	opentracing "github.com/opentracing/opentracing-go"
	"github.com/opentracing/opentracing-go/ext"
	otlog "github.com/opentracing/opentracing-go/log"
	"github.com/urfave/cli"
)

func AttachAppContext(app *cli.App) error {
	ctx := appcontext.Context()

	tracer, err := detect.OTTracer()
	if err != nil {
		return err
	}

	var span opentracing.Span

	for i, cmd := range app.Commands {
		func(before cli.BeforeFunc) {
			name := cmd.Name
			app.Commands[i].Before = func(clicontext *cli.Context) error {
				if before != nil {
					if err := before(clicontext); err != nil {
						return err
					}
				}

				span = tracer.StartSpan(name)
				span.LogFields(otlog.String("command", strings.Join(os.Args, " ")))

				ctx = opentracing.ContextWithSpan(ctx, span)

				clicontext.App.Metadata["context"] = ctx
				return nil
			}
		}(cmd.Before)
	}

	app.ExitErrHandler = func(clicontext *cli.Context, err error) {
		if span != nil {
			ext.Error.Set(span, true)
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
			span.Finish()
		}
		return detect.Shutdown(context.TODO())
	}
	return nil
}

func CommandContext(c *cli.Context) context.Context {
	return c.App.Metadata["context"].(context.Context)
}

type nopCloser struct {
}

func (*nopCloser) Close() error {
	return nil
}
