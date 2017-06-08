package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/Sirupsen/logrus"
	"github.com/containerd/containerd/sys"
	"github.com/pkg/errors"
	"github.com/urfave/cli"
	"golang.org/x/sys/unix"
	"google.golang.org/grpc"
)

func main() {
	app := cli.NewApp()
	app.Name = "buildd"
	app.Usage = "build daemon"

	app.Flags = []cli.Flag{
		cli.BoolFlag{
			Name:  "debug",
			Usage: "enable debug output in logs",
		},
		cli.StringFlag{
			Name:  "root",
			Usage: "path to state directory",
			Value: ".buildstate",
		},
		cli.StringFlag{
			Name:  "socket",
			Usage: "listening socket",
			Value: "/run/buildkit/buildd.sock",
		},
	}

	app.Action = func(c *cli.Context) error {
		signals := make(chan os.Signal, 2048)
		signal.Notify(signals, unix.SIGTERM, unix.SIGINT)

		server := grpc.NewServer()

		root := c.GlobalString("root")

		if err := os.MkdirAll(root, 0700); err != nil {
			return errors.Wrapf(err, "failed to create %s", root)
		}

		controller, err := newController(c)
		if err != nil {
			return err
		}

		controller.Register(server)

		ctx, cancel := context.WithCancel(context.Background())
		if err := serveGRPC(c.GlobalString("socket"), server, cancel); err != nil {
			return err
		}

		return handleSignals(ctx, signals, server)
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

func serveGRPC(path string, server *grpc.Server, cancel func()) error {
	if path == "" {
		return errors.New("--socket path cannot be empty")
	}
	l, err := sys.GetLocalListener(path, os.Getuid(), os.Getgid())
	if err != nil {
		return err
	}
	go func() {
		defer l.Close()
		logrus.Infof("running server on %s", path)
		if err := server.Serve(l); err != nil {
			logrus.WithError(err).Fatal("serve GRPC")
			cancel()
		}
	}()
	return nil
}

func handleSignals(ctx context.Context, signals chan os.Signal, server *grpc.Server) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-signals:
		logrus.Infof("stopping server")
		server.Stop()
		return nil
	}
}
