package main

import (
	"context"
	"log"

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

	log.Printf("%+v", du)

	return nil
}
