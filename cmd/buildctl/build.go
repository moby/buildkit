package main

import (
	"context"
	"os"

	"github.com/tonistiigi/buildkit_poc/client"
	"github.com/tonistiigi/buildkit_poc/util/progress/progressui"
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

	ch := make(chan *client.SolveStatus)
	eg, ctx := errgroup.WithContext(context.TODO()) // TODO: define appContext

	eg.Go(func() error {
		return c.Solve(ctx, os.Stdin, ch)
	})

	eg.Go(func() error {
		return progressui.DisplaySolveStatus(ctx, ch)
		// for s := range ch {
		// 		for _, v := range s.Vertexes {
		// 			log.Print(spew.Sdump(v))
		// 		}
		// 		for _, v := range s.Statuses {
		// 			log.Print(spew.Sdump(v))
		// 		}
		// 	}
		// return nil
	})

	return eg.Wait()
}
