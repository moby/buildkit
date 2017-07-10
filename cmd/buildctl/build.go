package main

import (
	"context"
	"encoding/json"
	"io/ioutil"
	"os"
	"strings"

	"github.com/Sirupsen/logrus"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/util/appcontext"
	"github.com/moby/buildkit/util/progress/progressui"
	"github.com/pkg/errors"
	"github.com/urfave/cli"
	"golang.org/x/sync/errgroup"
)

var buildCommand = cli.Command{
	Name:   "build",
	Usage:  "build",
	Action: build,
	Flags: []cli.Flag{
		cli.StringFlag{
			Name:  "exporter",
			Usage: "Define exporter for build result",
		},
		cli.StringSliceFlag{
			Name:  "exporter-opt",
			Usage: "Define custom options for exporter",
		},
	},
}

func build(clicontext *cli.Context) error {
	c, err := resolveClient(clicontext)
	if err != nil {
		return err
	}

	traceFile, err := ioutil.TempFile("", "buildctl")
	if err != nil {
		return err
	}
	defer traceFile.Close()
	traceEnc := json.NewEncoder(traceFile)

	logrus.Infof("tracing logs to %s", traceFile.Name())

	ch := make(chan *client.SolveStatus)
	displayCh := make(chan *client.SolveStatus)
	eg, ctx := errgroup.WithContext(appcontext.Context())

	exporterAttrs, err := attrMap(clicontext.StringSlice("exporter-opt"))
	if err != nil {
		return errors.Wrap(err, "invalid exporter-opt")
	}

	eg.Go(func() error {
		return c.Solve(ctx, os.Stdin, ch, clicontext.String("exporter"), exporterAttrs)
	})

	eg.Go(func() error {
		defer close(displayCh)
		for s := range ch {
			if err := traceEnc.Encode(s); err != nil {
				logrus.Error(err)
			}
			displayCh <- s
		}
		return nil
	})

	eg.Go(func() error {
		// not using shared context to not disrupt display but let is finish reporting errors
		return progressui.DisplaySolveStatus(context.TODO(), displayCh)
	})

	return eg.Wait()
}

func attrMap(sl []string) (map[string]string, error) {
	m := map[string]string{}
	for _, v := range sl {
		parts := strings.SplitN(v, "=", 2)
		if len(parts) != 2 {
			return nil, errors.Errorf("invalid value %s", v)
		}
		m[parts[0]] = parts[1]
	}
	return m, nil
}
