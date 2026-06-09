package main

import (
	"context"
	"fmt"
	"os"

	_ "github.com/moby/buildkit/client/connhelper/dockercontainer"
	_ "github.com/moby/buildkit/client/connhelper/kubepod"
	_ "github.com/moby/buildkit/client/connhelper/nerdctlcontainer"
	_ "github.com/moby/buildkit/client/connhelper/npipe"
	_ "github.com/moby/buildkit/client/connhelper/podmancontainer"
	_ "github.com/moby/buildkit/client/connhelper/ssh"
	bccommon "github.com/moby/buildkit/cmd/buildctl/common"
	"github.com/moby/buildkit/solver/errdefs"
	"github.com/moby/buildkit/sourcepolicy/policysession"
	"github.com/moby/buildkit/util/apicaps"
	"github.com/moby/buildkit/util/appdefaults"
	_ "github.com/moby/buildkit/util/grpcutil/encoding/proto"
	"github.com/moby/buildkit/util/profiler"
	"github.com/moby/buildkit/util/stack"
	_ "github.com/moby/buildkit/util/tracing/childprocess"
	_ "github.com/moby/buildkit/util/tracing/detect/jaeger"
	"github.com/moby/buildkit/version"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli/v3"
	"go.opentelemetry.io/otel"
)

func init() {
	apicaps.ExportedProduct = "buildkit"

	stack.SetVersionInfo(version.Version, version.Revision)

	// do not log tracing errors to stdio
	otel.SetErrorHandler(skipErrors{})
}

func main() {
	cli.VersionPrinter = func(c *cli.Command) {
		fmt.Println(c.Name, version.Package, c.Version, version.Revision)
	}
	app := &cli.Command{}
	app.Name = "buildctl"
	app.Usage = "build utility"
	app.Version = version.Version
	app.EnableShellCompletion = true

	defaultAddress := os.Getenv("BUILDKIT_HOST")
	if defaultAddress == "" {
		defaultAddress = appdefaults.Address
	}

	app.Flags = []cli.Flag{
		&cli.BoolFlag{
			Name:  "debug",
			Usage: "enable debug output in logs",
		},
		&cli.StringFlag{
			Name:  "addr",
			Usage: "buildkitd address",
			Value: defaultAddress,
		},
		// Add format flag to control log formatter
		&cli.StringFlag{
			Name:  "log-format",
			Usage: "log formatter: json or text",
			Value: "text",
		},
		&cli.StringFlag{
			Name:  "tlsservername",
			Usage: "buildkitd server name for certificate validation",
			Value: "",
		},
		&cli.StringFlag{
			Name:  "tlscacert",
			Usage: "CA certificate for validation",
			Value: "",
		},
		&cli.StringFlag{
			Name:  "tlscert",
			Usage: "client certificate",
			Value: "",
		},
		&cli.StringFlag{
			Name:  "tlskey",
			Usage: "client key",
			Value: "",
		},
		&cli.StringFlag{
			Name:  "tlsdir",
			Usage: "directory containing CA certificate, client certificate, and client key. Supported file names are (ca.pem, cert.pem, key.pem) or (ca.crt, tls.crt, tls.key)",
			Value: "",
		},
		&cli.IntFlag{
			Name:  "timeout",
			Usage: "timeout backend connection after value seconds",
			Value: 5,
		},
		&cli.BoolFlag{
			Name:  "wait",
			Usage: "block RPCs until the connection becomes available",
		},
	}

	app.Commands = []*cli.Command{
		diskUsageCommand,
		pruneCommand,
		pruneHistoriesCommand,
		buildCommand,
		debugCommand,
		dialStdioCommand,
	}
	disableSliceFlagSeparator(app)

	var debugEnabled bool

	app.Before = func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
		debugEnabled = cmd.Bool("debug")
		// Use Format flag to control log formatter
		logFormat := cmd.String("log-format")
		switch logFormat {
		case "json":
			logrus.SetFormatter(&logrus.JSONFormatter{})
		case "text", "":
			logrus.SetFormatter(&logrus.TextFormatter{FullTimestamp: true})
		default:
			return ctx, errors.Errorf("unsupported log type %q", logFormat)
		}
		if debugEnabled {
			logrus.SetLevel(logrus.DebugLevel)
		}
		return ctx, nil
	}

	if err := bccommon.AttachAppContext(app); err != nil {
		handleErr(debugEnabled, err)
	}

	profiler.Attach(app)

	handleErr(debugEnabled, app.Run(context.Background(), os.Args))
}

func handleErr(debug bool, err error) {
	if err == nil {
		return
	}
	for _, s := range errdefs.Sources(err) {
		s.Print(os.Stderr)
	}
	for _, msg := range policysession.DenyMessages(err) {
		if msg.GetMessage() != "" {
			fmt.Fprintf(os.Stderr, "policy deny: %s\n", msg.GetMessage())
		}
	}
	if debug {
		fmt.Fprintf(os.Stderr, "error: %+v", stack.Formatter(err))
	} else {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
	}
	os.Exit(1)
}

type skipErrors struct{}

func (skipErrors) Handle(err error) {}

func commandAction(fn func(*cli.Command) error) cli.ActionFunc {
	return func(_ context.Context, cmd *cli.Command) error {
		return fn(cmd)
	}
}

func disableSliceFlagSeparator(cmd *cli.Command) {
	cmd.DisableSliceFlagSeparator = true
	for _, subcmd := range cmd.Commands {
		disableSliceFlagSeparator(subcmd)
	}
}
