package main

import (
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	units "github.com/docker/go-units"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/util/appcontext"
	"github.com/urfave/cli"
)

var diskUsageCommand = cli.Command{
	Name:   "du",
	Usage:  "disk usage",
	Action: diskUsage,
	Flags: []cli.Flag{
		cli.StringFlag{
			Name:  "filter, f",
			Usage: "Filter snapshot ID",
		},
		cli.BoolFlag{
			Name:  "verbose, v",
			Usage: "Verbose output",
		},
	},
}

func diskUsage(clicontext *cli.Context) error {
	c, err := resolveClient(clicontext)
	if err != nil {
		return err
	}

	du, err := c.DiskUsage(appcontext.Context(), client.WithFilter(clicontext.String("filter")))
	if err != nil {
		return err
	}

	tw := tabwriter.NewWriter(os.Stdout, 1, 8, 1, '\t', 0)

	if clicontext.Bool("verbose") {
		printVerbose(tw, du)
	} else {
		printTable(tw, du)
	}

	if clicontext.String("filter") == "" {
		printSummary(tw, du)
	}

	return nil
}

func printKV(w io.Writer, k string, v interface{}) {
	fmt.Fprintf(w, "%s:\t%v\n", k, v)
}

func printVerbose(tw *tabwriter.Writer, du []*client.UsageInfo) {
	for _, di := range du {
		printKV(tw, "ID", di.ID)
		if di.Parent != "" {
			printKV(tw, "Parent", di.Parent)
		}
		printKV(tw, "Created at", di.CreatedAt)
		printKV(tw, "Mutable", di.Mutable)
		printKV(tw, "Reclaimable", !di.InUse)
		printKV(tw, "Size", units.HumanSize(float64(di.Size)))
		if di.Description != "" {
			printKV(tw, "Description", di.Description)
		}
		printKV(tw, "Usage count", di.UsageCount)
		if di.LastUsedAt != nil {
			printKV(tw, "Last used", di.LastUsedAt)
		}

		fmt.Fprintf(tw, "\n")
	}

	tw.Flush()
}

func printTable(tw *tabwriter.Writer, du []*client.UsageInfo) {
	fmt.Fprintln(tw, "ID\tRECLAIMABLE\tSIZE\tLAST ACCESSED")

	for _, di := range du {
		id := di.ID
		if di.Mutable {
			id += "*"
		}
		fmt.Fprintf(tw, "%s\t%v\t%s\t\n", id, !di.InUse, units.HumanSize(float64(di.Size)))

	}

	tw.Flush()
}

func printSummary(tw *tabwriter.Writer, du []*client.UsageInfo) {
	total := int64(0)
	reclaimable := int64(0)

	for _, di := range du {
		if di.Size > 0 {
			total += di.Size
			if !di.InUse {
				reclaimable += di.Size
			}
		}
	}

	tw = tabwriter.NewWriter(os.Stdout, 1, 8, 1, '\t', 0)
	fmt.Fprintf(tw, "Reclaimable:\t%s\n", units.HumanSize(float64(reclaimable)))
	fmt.Fprintf(tw, "Total:\t%s\n", units.HumanSize(float64(total)))
	tw.Flush()
}
