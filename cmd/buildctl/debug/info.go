package debug

import (
	"fmt"
	"os"
	"text/tabwriter"

	bccommon "github.com/moby/buildkit/cmd/buildctl/common"
	"github.com/urfave/cli"
)

var InfoCommand = cli.Command{
	Name:   "info",
	Usage:  "display internal information",
	Action: info,
}

func info(clicontext *cli.Context) error {
	c, err := bccommon.ResolveClient(clicontext)
	if err != nil {
		return err
	}
	res, err := c.Info(bccommon.CommandContext(clicontext))
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)
	_, _ = fmt.Fprintf(w, "BuildKit:\t%s %s %s\n", res.BuildkitVersion.Package, res.BuildkitVersion.Version, res.BuildkitVersion.Revision)
	return w.Flush()
}
