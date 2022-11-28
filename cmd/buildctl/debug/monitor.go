package debug

import (
	"fmt"

	controlapi "github.com/moby/buildkit/api/services/control"
	bccommon "github.com/moby/buildkit/cmd/buildctl/common"
	"github.com/moby/buildkit/util/appcontext"
	"github.com/urfave/cli/v2"
)

var MonitorCommand = &cli.Command{
	Name:   "monitor",
	Usage:  "display build events",
	Action: monitor,
	Flags: []cli.Flag{
		&cli.BoolFlag{
			Name:  "completed",
			Usage: "show completed builds",
		},
		&cli.StringFlag{
			Name:  "ref",
			Usage: "show events for a specific build",
		},
	},
}

func monitor(clicontext *cli.Context) error {
	c, err := bccommon.ResolveClient(clicontext)
	if err != nil {
		return err
	}
	completed := clicontext.Bool("completed")

	ctx := appcontext.Context()

	cl, err := c.ControlClient().ListenBuildHistory(ctx, &controlapi.BuildHistoryRequest{
		ActiveOnly: !completed,
		Ref:        clicontext.String("ref"),
	})
	if err != nil {
		return err
	}

	for {
		ev, err := cl.Recv()
		if err != nil {
			return err
		}
		fmt.Printf("%s %s\n", ev.Type.String(), ev.Record.Ref)
	}
}
