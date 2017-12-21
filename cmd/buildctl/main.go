package main

import (
	"fmt"
	"net/url"
	"os"

	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/util/appdefaults"
	"github.com/moby/buildkit/util/profiler"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli"
)

func main() {
	app := cli.NewApp()
	app.Name = "buildctl"
	app.Usage = "build utility"

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
	}

	app.Commands = []cli.Command{
		diskUsageCommand,
		buildCommand,
		debugCommand,
	}

	var debugEnabled bool

	app.Before = func(context *cli.Context) error {
		debugEnabled = context.GlobalBool("debug")
		if debugEnabled {
			logrus.SetLevel(logrus.DebugLevel)
		}
		return nil
	}

	profiler.Attach(app)

	if err := app.Run(os.Args); err != nil {
		if debugEnabled {
			fmt.Fprintf(os.Stderr, "error: %+v\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
		}
		os.Exit(1)
	}
}

func resolveClient(c *cli.Context) (*client.Client, error) {
	serverName := c.GlobalString("tlsservername")
	if serverName == "" {
		// guess servername as hostname of target address
		uri, err := url.Parse(c.GlobalString("addr"))
		if err != nil {
			return nil, err
		}
		serverName = uri.Hostname()
	}
	caCert := c.GlobalString("tlscacert")
	cert := c.GlobalString("tlscert")
	key := c.GlobalString("tlskey")
	opts := []client.ClientOpt{client.WithBlock()}
	if caCert != "" || cert != "" || key != "" {
		opts = append(opts, client.WithCredentials(serverName, caCert, cert, key))
	}
	return client.New(c.GlobalString("addr"), opts...)
}
