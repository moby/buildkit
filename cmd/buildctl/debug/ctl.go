package debug

import (
	"errors"

	controlapi "github.com/moby/buildkit/api/services/control"
	bccommon "github.com/moby/buildkit/cmd/buildctl/common"
	"github.com/moby/buildkit/util/appcontext"
	"github.com/urfave/cli"
)

var CtlCommand = cli.Command{
	Name:   "ctl",
	Usage:  "control build records",
	Action: ctl,
	Flags: []cli.Flag{
		cli.BoolFlag{
			Name:  "pin",
			Usage: "Pin build so it will not be garbage collected",
		},
		cli.BoolFlag{
			Name:  "unpin",
			Usage: "Unpin build so it will be garbage collected",
		},
		cli.BoolFlag{
			Name:  "delete",
			Usage: "Delete build record",
		},
	},
}

func ctl(clicontext *cli.Context) error {
	args := clicontext.Args()
	if len(args) == 0 {
		return errors.New("build ref must be specified")
	}
	ref := args[0]

	c, err := bccommon.ResolveClient(clicontext)
	if err != nil {
		return err
	}

	ctx := appcontext.Context()

	pin := clicontext.Bool("pin")
	unpin := clicontext.Bool("unpin")
	del := clicontext.Bool("delete")

	if !pin && !unpin && !del {
		return errors.New("must specify one of --pin, --unpin, --delete")
	}

	if pin && unpin {
		return errors.New("cannot specify both --pin and --unpin")
	}

	if del && (pin || unpin) {
		return errors.New("cannot specify --delete with --pin or --unpin")
	}

	_, err = c.ControlClient().UpdateBuildHistory(ctx, &controlapi.UpdateBuildHistoryRequest{
		Ref:    ref,
		Pinned: pin,
		Delete: del,
	})
	return err
}
