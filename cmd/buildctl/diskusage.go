package main

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"

	units "github.com/docker/go-units"
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

	tw := tabwriter.NewWriter(os.Stdout, 1, 8, 1, '\t', 0)

	fmt.Fprintln(tw, "ID\tRECLAIMABLE\tSIZE\tLAST ACCESSED")

	total := int64(0)
	reclaimable := int64(0)

	for _, di := range du {
		id := di.ID
		if di.Mutable {
			id += "*"
		}
		fmt.Fprintf(tw, "%s\t%v\t%s\t\n", id, !di.InUse, units.HumanSize(float64(di.Size)))
		if di.Size > 0 {
			total += di.Size
			if !di.InUse {
				reclaimable += di.Size
			}
		}
	}

	tw.Flush()

	tw = tabwriter.NewWriter(os.Stdout, 1, 8, 1, '\t', 0)
	fmt.Fprintf(tw, "Reclaimable:\t%s\n", units.HumanSize(float64(reclaimable)))
	fmt.Fprintf(tw, "Total:\t%s\n", units.HumanSize(float64(total)))
	tw.Flush()

	return nil
}
