package debug

import (
	"context"
	"fmt"
	"os"
	"sort"
	"text/tabwriter"

	"github.com/moby/buildkit/client"
	"github.com/urfave/cli"
)

var WorkersCommand = cli.Command{
	Name:   "workers",
	Usage:  "list workers",
	Action: listWorkers,
	Flags: []cli.Flag{
		cli.StringSliceFlag{
			Name:  "filter, f",
			Usage: "containerd-style filter string slice",
		},
		cli.BoolFlag{
			Name:  "verbose, v",
			Usage: "Verbose output",
		},
	},
}

func resolveClient(c *cli.Context) (*client.Client, error) {
	return client.New(c.GlobalString("addr"), client.WithBlock())
}

func listWorkers(clicontext *cli.Context) error {
	c, err := resolveClient(clicontext)
	if err != nil {
		return err
	}

	workers, err := c.ListWorkers(commandContext(clicontext), client.WithWorkerFilter(clicontext.StringSlice("filter")))
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(os.Stdout, 1, 8, 1, '\t', 0)

	if clicontext.Bool("verbose") {
		printWorkersVerbose(tw, workers)
	} else {
		printWorkersTable(tw, workers)
	}
	return nil
}

func printWorkersVerbose(tw *tabwriter.Writer, winfo []*client.WorkerInfo) {
	for _, wi := range winfo {
		fmt.Fprintf(tw, "ID:\t%s\n", wi.ID)
		fmt.Fprintf(tw, "Labels:\n")
		for _, k := range sortedKeys(wi.Labels) {
			v := wi.Labels[k]
			fmt.Fprintf(tw, "\t%s:\t%s\n", k, v)
		}
		fmt.Fprintf(tw, "\n")
	}

	tw.Flush()
}

func printWorkersTable(tw *tabwriter.Writer, winfo []*client.WorkerInfo) {
	fmt.Fprintln(tw, "ID")

	for _, wi := range winfo {
		id := wi.ID
		fmt.Fprintf(tw, "%s\n", id)
	}

	tw.Flush()
}

func sortedKeys(m map[string]string) []string {
	s := make([]string, len(m))
	i := 0
	for k := range m {
		s[i] = k
		i++
	}
	sort.Strings(s)
	return s
}

func commandContext(c *cli.Context) context.Context {
	return c.App.Metadata["context"].(context.Context)
}
