package debug

import (
	"io"
	"os"

	controlapi "github.com/moby/buildkit/api/services/control"
	"github.com/moby/buildkit/client"
	bccommon "github.com/moby/buildkit/cmd/buildctl/common"
	"github.com/moby/buildkit/util/appcontext"
	"github.com/moby/buildkit/util/progress/progresswriter"
	"github.com/pkg/errors"
	"github.com/urfave/cli"
)

var LogsCommand = cli.Command{
	Name:   "logs",
	Usage:  "display build logs",
	Action: logs,
	Flags: []cli.Flag{
		cli.StringFlag{
			Name:  "progress",
			Usage: "progress output type",
			Value: "auto",
		},
	},
}

func logs(clicontext *cli.Context) error {
	args := clicontext.Args()
	if len(args) == 0 {
		return errors.Errorf("build ref must be specified")
	}
	ref := args[0]

	c, err := bccommon.ResolveClient(clicontext)
	if err != nil {
		return err
	}

	ctx := appcontext.Context()

	cl, err := c.ControlClient().Status(ctx, &controlapi.StatusRequest{
		Ref: ref,
	})
	if err != nil {
		return err
	}

	pw, err := progresswriter.NewPrinter(ctx, os.Stdout, clicontext.String("progress"))
	if err != nil {
		return err
	}

	defer func() {
		<-pw.Done()
	}()

	for {
		resp, err := cl.Recv()
		if err != nil {
			close(pw.Status())
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		pw.Status() <- client.NewSolveStatus(resp)
	}
}
