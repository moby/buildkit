package main

import (
	"context"
	"log"

	"github.com/davecgh/go-spew/spew"
	"github.com/urfave/cli"
)

var diskUsageCommand = cli.Command{
	Name:   "du",
	Usage:  "disk usage",
	Action: diskUsage,
}

func diskUsage(clicontext *cli.Context) error {
	client, err := resolveClient(clicontext)
	if err != nil {
		return err
	}

	du, err := client.DiskUsage(context.TODO())
	if err != nil {
		return err
	}

	log.Printf(spew.Sdump(du))

	return nil
}
