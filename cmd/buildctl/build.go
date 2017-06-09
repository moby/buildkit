package main

import (
	"context"
	"os"

	"github.com/urfave/cli"
)

var buildCommand = cli.Command{
	Name:   "build",
	Usage:  "build",
	Action: build,
}

func build(clicontext *cli.Context) error {
	client, err := resolveClient(clicontext)
	if err != nil {
		return err
	}
	return client.Solve(context.TODO(), os.Stdin)
}
