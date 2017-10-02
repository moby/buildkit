package main

import (
	"context"
	"encoding/json"
	"io"
	"io/ioutil"
	"os"
	"strings"

	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/appcontext"
	"github.com/moby/buildkit/util/progress/progressui"
	"github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
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
		cli.BoolFlag{
			Name:  "no-progress",
			Usage: "Don't show interactive progress",
		},
		cli.StringFlag{
			Name:  "trace",
			Usage: "Path to trace file. e.g. /dev/null. Defaults to /tmp/buildctlXXXXXXXXX.",
		},
		cli.StringSliceFlag{
			Name:  "local",
			Usage: "Allow build access to the local directory",
		},
		cli.StringFlag{
			Name:  "frontend",
			Usage: "Define frontend used for build",
		},
		cli.StringSliceFlag{
			Name:  "frontend-opt",
			Usage: "Define custom options for frontend",
		},
		cli.BoolFlag{
			Name:  "no-cache",
			Usage: "Disable cache for all the vertices. (Not yet implemented.) Frontend is not supported.",
		},
	},
}

func read(r io.Reader, clicontext *cli.Context) (*llb.Definition, error) {
	def, err := llb.ReadFrom(r)
	if err != nil {
		return nil, errors.Wrap(err, "failed to parse input")
	}
	if clicontext.Bool("no-cache") {
		for _, dt := range def.Def {
			var op pb.Op
			if err := (&op).Unmarshal(dt); err != nil {
				return nil, errors.Wrap(err, "failed to parse llb proto op")
			}
			dgst := digest.FromBytes(dt)
			opMetadata, ok := def.Metadata[dgst]
			if !ok {
				opMetadata = llb.OpMetadata{}
			}
			opMetadata.IgnoreCache = true
			def.Metadata[dgst] = opMetadata
		}
	}
	return def, nil
}

func openTraceFile(clicontext *cli.Context) (*os.File, error) {
	if traceFileName := clicontext.String("trace"); traceFileName != "" {
		return os.OpenFile(traceFileName, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	}
	return ioutil.TempFile("", "buildctl")
}

func build(clicontext *cli.Context) error {
	c, err := resolveClient(clicontext)
	if err != nil {
		return err
	}

	traceFile, err := openTraceFile(clicontext)
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

	frontendAttrs, err := attrMap(clicontext.StringSlice("frontend-opt"))
	if err != nil {
		return errors.Wrap(err, "invalid frontend-opt")
	}

	localDirs, err := attrMap(clicontext.StringSlice("local"))
	if err != nil {
		return errors.Wrap(err, "invalid local")
	}

	var def *llb.Definition
	if clicontext.String("frontend") == "" {
		def, err = read(os.Stdin, clicontext)
		if err != nil {
			return err
		}
	} else {
		if clicontext.Bool("no-cache") {
			return errors.New("no-cache is not supported for frontends")
		}
	}

	eg.Go(func() error {
		return c.Solve(ctx, def, client.SolveOpt{
			Exporter:      clicontext.String("exporter"),
			ExporterAttrs: exporterAttrs,
			LocalDirs:     localDirs,
			Frontend:      clicontext.String("frontend"),
			FrontendAttrs: frontendAttrs,
		}, ch)
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
		if clicontext.Bool("no-progress") {
			for s := range displayCh {
				for _, v := range s.Vertexes {
					logrus.Debugf("vertex: %s %s %v %v", v.Digest, v.Name, v.Started, v.Completed)
				}
				for _, s := range s.Statuses {
					logrus.Debugf("status: %s %s %d", s.Vertex, s.ID, s.Current)
				}
				for _, l := range s.Logs {
					logrus.Debugf("log: %s\n%s", l.Vertex, l.Data)
				}
			}
			return nil
		}
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
