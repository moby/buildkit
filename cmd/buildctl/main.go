package main

import (
	"fmt"
	"os"

	_ "github.com/moby/buildkit/client/connhelper/dockercontainer"
	_ "github.com/moby/buildkit/client/connhelper/kubepod"
	_ "github.com/moby/buildkit/client/connhelper/podmancontainer"
	bccommon "github.com/moby/buildkit/cmd/buildctl/common"
	"github.com/moby/buildkit/solver/errdefs"
	"github.com/moby/buildkit/util/apicaps"
	"github.com/moby/buildkit/util/appdefaults"
	"github.com/moby/buildkit/util/profiler"
	"github.com/moby/buildkit/util/stack"
	"github.com/moby/buildkit/version"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli"
)

func init() {
	apicaps.ExportedProduct = "buildkit"

	stack.SetVersionInfo(version.Version, version.Revision)
}

func main() {
	cli.VersionPrinter = func(c *cli.Context) {
		fmt.Println(c.App.Name, version.Package, c.App.Version, version.Revision)
	}
	app := cli.NewApp()
	app.Name = "buildctl"
	app.Usage = "build utility"
	app.Version = version.Version

	defaultAddress := os.Getenv("BUILDKIT_HOST")
	if defaultAddress == "" {
		defaultAddress = appdefaults.Address
	}

	app.Flags = []cli.Flag{
		cli.BoolFlag{
			Name:  "debug",
			Usage: "enable debug output in logs",
		},
		cli.StringFlag{
			Name:  "addr",
			Usage: "buildkitd address",
			Value: defaultAddress,
		},
		cli.StringFlag{
			Name:  "tlsservername",
			Usage: "buildkitd server name for certificate validation",
			Value: "",
		},
		cli.StringFlag{
			Name:  "tlscacert",
			Usage: "CA certificate for validation",
			Value: "",
		},
		cli.StringFlag{
			Name:  "tlscert",
			Usage: "client certificate",
			Value: "",
		},
		cli.StringFlag{
			Name:  "tlskey",
			Usage: "client key",
			Value: "",
		},
		cli.StringFlag{
			Name:  "tlsdir",
			Usage: "directory containing CA certificate, client certificate, and client key",
			Value: "",
		},
		cli.IntFlag{
			Name:  "timeout",
			Usage: "timeout backend connection after value seconds",
			Value: 5,
		},
	}

	app.Commands = []cli.Command{
		diskUsageCommand,
		pruneCommand,
		buildCommand,
		debugCommand,
		dialStdioCommand,
	}

	var debugEnabled bool

	app.Before = func(context *cli.Context) error {
		debugEnabled = context.GlobalBool("debug")
		if debugEnabled {
			logrus.SetLevel(logrus.DebugLevel)
		}
		return nil
	}

	bccommon.AttachAppContext(app)

	profiler.Attach(app)

	if err := app.Run(os.Args); err != nil {
		for _, s := range errdefs.Sources(err) {
			s.Print(os.Stderr)
		}
		if debugEnabled {
			fmt.Fprintf(os.Stderr, "error: %+v", stack.Formatter(err))
		} else {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
		}
		os.Exit(1)
	}
}
