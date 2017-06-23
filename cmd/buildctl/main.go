package main

import (
	"fmt"
	"os"

	"github.com/Sirupsen/logrus"
	"github.com/moby/buildkit/client"
	"github.com/urfave/cli"
)

func main() {
	app := cli.NewApp()
	app.Name = "buildctl"
	app.Usage = "build utility"

	app.Flags = []cli.Flag{
		cli.BoolFlag{
			Name:  "debug",
			Usage: "enable debug output in logs",
		},
		cli.StringFlag{
			Name:  "socket",
			Usage: "listening socket",
			Value: "/run/buildkit/buildd.sock",
		},
	}

	app.Commands = []cli.Command{
		diskUsageCommand,
		buildCommand,
		debugCommand,
	}

	app.Before = func(context *cli.Context) error {
		if context.GlobalBool("debug") {
			logrus.SetLevel(logrus.DebugLevel)
		}
		return nil
	}
	if err := app.Run(os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "buildd: %s\n", err)
		os.Exit(1)
	}
}

func resolveClient(c *cli.Context) (*client.Client, error) {
	return client.New(c.GlobalString("socket"), client.WithBlock())
}
