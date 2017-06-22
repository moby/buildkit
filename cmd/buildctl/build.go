package main

import (
	"context"
	"encoding/json"
	"io/ioutil"
	"os"

	"github.com/Sirupsen/logrus"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/util/progress/progressui"
	"github.com/urfave/cli"
	"golang.org/x/sync/errgroup"
)

var buildCommand = cli.Command{
	Name:   "build",
	Usage:  "build",
	Action: build,
}

func build(clicontext *cli.Context) error {
	c, err := resolveClient(clicontext)
	if err != nil {
		return err
	}

	traceFile, err := ioutil.TempFile("", "buildctl")
	if err != nil {
		return err
	}
	defer traceFile.Close()
	traceEnc := json.NewEncoder(traceFile)

	logrus.Infof("tracing logs to %s", traceFile.Name())

	ch := make(chan *client.SolveStatus)
	displayCh := make(chan *client.SolveStatus)
	eg, ctx := errgroup.WithContext(context.TODO()) // TODO: define appContext

	eg.Go(func() error {
		return c.Solve(ctx, os.Stdin, ch)
	})

	eg.Go(func() error {
		defer close(displayCh)
		for s := range ch {
			if err := traceEnc.Encode(s); err != nil {
				logrus.Error(err)
			}
			displayCh <- s
		}
		return nil
	})

	eg.Go(func() error {
		return progressui.DisplaySolveStatus(ctx, displayCh)
	})

	return eg.Wait()
}
