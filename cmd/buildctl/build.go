package main

import (
	"context"
	"encoding/json"
	"io"
	"os"

	"github.com/containerd/console"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/cmd/buildctl/build"
	bccommon "github.com/moby/buildkit/cmd/buildctl/common"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/auth/authprovider"
	"github.com/moby/buildkit/session/sshforward/sshprovider"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/progress/progressui"
	"github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli"
	"golang.org/x/sync/errgroup"
)

var buildCommand = cli.Command{
	Name:    "build",
	Aliases: []string{"b"},
	Usage:   "build",
	UsageText: `
	To build and push an image using Dockerfile:
	  $ buildctl build --frontend dockerfile.v0 --opt target=foo --opt build-arg:foo=bar --local context=. --local dockerfile=. --output type=image,name=docker.io/username/image,push=true
	`,
	Action: buildAction,
	Flags: []cli.Flag{
		cli.StringSliceFlag{
			Name:  "output,o",
			Usage: "Define exports for build result, e.g. --output type=image,name=docker.io/username/image,push=true",
		},
		cli.StringFlag{
			Name:   "exporter",
			Usage:  "Define exporter for build result (DEPRECATED: use --export type=<type>[,<opt>=<optval>]",
			Hidden: true,
		},
		cli.StringSliceFlag{
			Name:   "exporter-opt",
			Usage:  "Define custom options for exporter (DEPRECATED: use --output type=<type>[,<opt>=<optval>]",
			Hidden: true,
		},
		cli.StringFlag{
			Name:  "progress",
			Usage: "Set type of progress (auto, plain, tty). Use plain to show container output",
			Value: "auto",
		},
		cli.StringFlag{
			Name:  "trace",
			Usage: "Path to trace file. Defaults to no tracing.",
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
			Name:  "opt",
			Usage: "Define custom options for frontend, e.g. --opt target=foo --opt build-arg:foo=bar",
		},
		cli.StringSliceFlag{
			Name:   "frontend-opt",
			Usage:  "Define custom options for frontend, e.g. --frontend-opt target=foo --frontend-opt build-arg:foo=bar (DEPRECATED: use --opt)",
			Hidden: true,
		},
		cli.BoolFlag{
			Name:  "no-cache",
			Usage: "Disable cache for all the vertices",
		},
		cli.StringSliceFlag{
			Name:  "export-cache",
			Usage: "Export build cache, e.g. --export-cache type=registry,ref=example.com/foo/bar, or --export-cache type=local,dest=path/to/dir",
		},
		cli.StringSliceFlag{
			Name:   "export-cache-opt",
			Usage:  "Define custom options for cache exporting (DEPRECATED: use --export-cache type=<type>,<opt>=<optval>[,<opt>=<optval>]",
			Hidden: true,
		},
		cli.StringSliceFlag{
			Name:  "import-cache",
			Usage: "Import build cache, e.g. --import-cache type=registry,ref=example.com/foo/bar, or --import-cache type=local,src=path/to/dir",
		},
		cli.StringSliceFlag{
			Name:  "secret",
			Usage: "Secret value exposed to the build. Format id=secretname,src=filepath",
		},
		cli.StringSliceFlag{
			Name:  "allow",
			Usage: "Allow extra privileged entitlement, e.g. network.host, security.insecure",
		},
		cli.StringSliceFlag{
			Name:  "ssh",
			Usage: "Allow forwarding SSH agent to the builder. Format default|<id>[=<socket>|<key>[,<key>]]",
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
				opMetadata = pb.OpMetadata{}
			}
			c := llb.Constraints{Metadata: opMetadata}
			llb.IgnoreCache(&c)
			def.Metadata[dgst] = c.Metadata
		}
	}
	return def, nil
}

func openTraceFile(clicontext *cli.Context) (*os.File, error) {
	if traceFileName := clicontext.String("trace"); traceFileName != "" {
		return os.OpenFile(traceFileName, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	}
	return nil, nil
}

func buildAction(clicontext *cli.Context) error {
	c, err := bccommon.ResolveClient(clicontext)
	if err != nil {
		return err
	}

	traceFile, err := openTraceFile(clicontext)
	if err != nil {
		return err
	}
	var traceEnc *json.Encoder
	if traceFile != nil {
		defer traceFile.Close()
		traceEnc = json.NewEncoder(traceFile)

		logrus.Infof("tracing logs to %s", traceFile.Name())
	}

	attachable := []session.Attachable{authprovider.NewDockerAuthProvider()}

	if ssh := clicontext.StringSlice("ssh"); len(ssh) > 0 {
		configs, err := build.ParseSSH(ssh)
		if err != nil {
			return err
		}
		sp, err := sshprovider.NewSSHAgentProvider(configs)
		if err != nil {
			return err
		}
		attachable = append(attachable, sp)
	}

	if secrets := clicontext.StringSlice("secret"); len(secrets) > 0 {
		secretProvider, err := build.ParseSecret(secrets)
		if err != nil {
			return err
		}
		attachable = append(attachable, secretProvider)
	}

	allowed, err := build.ParseAllow(clicontext.StringSlice("allow"))
	if err != nil {
		return err
	}

	var exports []client.ExportEntry
	if legacyExporter := clicontext.String("exporter"); legacyExporter != "" {
		logrus.Warnf("--exporter <exporter> is deprecated. Please use --output type=<exporter>[,<opt>=<optval>] instead.")
		if len(clicontext.StringSlice("output")) > 0 {
			return errors.New("--exporter cannot be used with --output")
		}
		exports, err = build.ParseLegacyExporter(clicontext.String("exporter"), clicontext.StringSlice("exporter-opt"))
	} else {
		exports, err = build.ParseOutput(clicontext.StringSlice("output"))
	}
	if err != nil {
		return err
	}

	cacheExports, err := build.ParseExportCache(clicontext.StringSlice("export-cache"), clicontext.StringSlice("export-cache-opt"))
	if err != nil {
		return err
	}
	cacheImports, err := build.ParseImportCache(clicontext.StringSlice("import-cache"))
	if err != nil {
		return err
	}

	ch := make(chan *client.SolveStatus)
	eg, ctx := errgroup.WithContext(bccommon.CommandContext(clicontext))

	solveOpt := client.SolveOpt{
		Exports: exports,
		// LocalDirs is set later
		Frontend: clicontext.String("frontend"),
		// FrontendAttrs is set later
		CacheExports:        cacheExports,
		CacheImports:        cacheImports,
		Session:             attachable,
		AllowedEntitlements: allowed,
	}

	solveOpt.FrontendAttrs, err = build.ParseOpt(clicontext.StringSlice("opt"), clicontext.StringSlice("frontend-opt"))
	if err != nil {
		return errors.Wrap(err, "invalid opt")
	}

	solveOpt.LocalDirs, err = build.ParseLocal(clicontext.StringSlice("local"))
	if err != nil {
		return errors.Wrap(err, "invalid local")
	}

	var def *llb.Definition
	if clicontext.String("frontend") == "" {
		if fi, _ := os.Stdin.Stat(); (fi.Mode() & os.ModeCharDevice) != 0 {
			return errors.Errorf("please specify --frontend or pipe LLB definition to stdin")
		}
		def, err = read(os.Stdin, clicontext)
		if err != nil {
			return err
		}
		if len(def.Def) == 0 {
			return errors.Errorf("empty definition sent to build. Specify --frontend instead?")
		}
	} else {
		if clicontext.Bool("no-cache") {
			solveOpt.FrontendAttrs["no-cache"] = ""
		}
	}

	eg.Go(func() error {
		resp, err := c.Solve(ctx, def, solveOpt, ch)
		if err != nil {
			return err
		}
		for k, v := range resp.ExporterResponse {
			logrus.Debugf("exporter response: %s=%s", k, v)
		}
		return err
	})

	displayCh := ch
	if traceEnc != nil {
		displayCh = make(chan *client.SolveStatus)
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
	}

	eg.Go(func() error {
		var c console.Console
		progressOpt := clicontext.String("progress")

		switch progressOpt {
		case "auto", "tty":
			cf, err := console.ConsoleFromFile(os.Stderr)
			if err != nil && progressOpt == "tty" {
				return err
			}
			c = cf
		case "plain":
		default:
			return errors.Errorf("invalid progress value : %s", progressOpt)
		}

		// not using shared context to not disrupt display but let is finish reporting errors
		return progressui.DisplaySolveStatus(context.TODO(), "", c, os.Stderr, displayCh)
	})

	return eg.Wait()
}
