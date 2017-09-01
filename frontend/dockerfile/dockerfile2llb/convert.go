package dockerfile2llb

import (
	"bytes"
	"context"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/docker/distribution/reference"
	"github.com/docker/docker/builder/dockerfile/instructions"
	"github.com/docker/docker/builder/dockerfile/parser"
	"github.com/moby/buildkit/client/llb"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"
)

const (
	emptyImageName = "scratch"
)

type ConvertOpt struct {
	Target       string
	MetaResolver llb.ImageMetaResolver
}

func Dockerfile2LLB(ctx context.Context, dt []byte, opt ConvertOpt) (*llb.State, error) {
	if len(dt) == 0 {
		return nil, errors.Errorf("the Dockerfile cannot be empty")
	}

	dockerfile, err := parser.Parse(bytes.NewReader(dt))
	if err != nil {
		return nil, err
	}

	stages, metaArgs, err := instructions.Parse(dockerfile.AST)
	if err != nil {
		return nil, err
	}

	_ = metaArgs

	var allStages []*dispatchState
	stagesByName := map[string]*dispatchState{}

	for _, st := range stages {
		state := llb.Scratch()
		if st.BaseName != emptyImageName {
			state = llb.Image(st.BaseName)
		}

		ds := &dispatchState{
			state: state,
			stage: st,
		}
		if d, ok := stagesByName[st.BaseName]; ok {
			ds.base = d
		}

		allStages = append(allStages, ds)
		if st.Name != "" {
			stagesByName[strings.ToLower(st.Name)] = ds
		}
	}

	metaResolver := llb.DefaultImageMetaResolver()

	eg, ctx := errgroup.WithContext(ctx)
	for i, d := range allStages {
		if d.base == nil && d.stage.BaseName != emptyImageName {
			func(i int, d *dispatchState) {
				eg.Go(func() error {
					ref, err := reference.ParseNormalizedNamed(d.stage.BaseName)
					if err != nil {
						return err
					}
					d.stage.BaseName = reference.TagNameOnly(ref).String()
					if opt.MetaResolver != nil {
						img, err := metaResolver.Resolve(ctx, d.stage.BaseName)
						if err != nil {
							return err
						}
						allStages[i].image = *img
					}
					return nil
				})
			}(i, d)
		}
	}

	if err := eg.Wait(); err != nil {
		return nil, err
	}

	for _, d := range allStages {
		if d.base != nil {
			d.state = d.base.state
		}

		for _, env := range d.image.Config.Env {
			parts := strings.SplitN(env, "=", 2)
			v := ""
			if len(parts) > 1 {
				v = parts[1]
			}
			if err := dispatchEnv(d, &instructions.EnvCommand{Env: []instructions.KeyValuePair{{Key: parts[0], Value: v}}}); err != nil {
				return nil, err
			}
		}
		if d.image.Config.WorkingDir != "" {
			if err = dispatchWorkdir(d, &instructions.WorkdirCommand{Path: d.image.Config.WorkingDir}); err != nil {
				return nil, err
			}
		}

		for _, cmd := range d.stage.Commands {
			switch c := cmd.(type) {
			case *instructions.EnvCommand:
				err = dispatchEnv(d, c)
			case *instructions.RunCommand:
				err = dispatchRun(d, c)
			case *instructions.WorkdirCommand:
				err = dispatchWorkdir(d, c)
			case *instructions.AddCommand:
				err = dispatchCopy(d, c.SourcesAndDest, llb.Local("context"))
			case *instructions.CopyCommand:
				l := llb.Local("context")
				if c.From != "" {
					index, err := strconv.Atoi(c.From)
					if err != nil {
						stn, ok := stagesByName[strings.ToLower(c.From)]
						if !ok {
							return nil, errors.Errorf("stage %s not found", c.From)
						}
						l = stn.state
					} else {
						if index >= len(allStages) {
							return nil, errors.Errorf("invalid stage index %d", index)
						}
						l = allStages[index].state
					}
				}
				err = dispatchCopy(d, c.SourcesAndDest, l)
			default:
				continue
			}
			if err != nil {
				return nil, err
			}
		}

	}

	if opt.Target == "" {
		return &allStages[len(allStages)-1].state, nil
	}

	state, ok := stagesByName[strings.ToLower(opt.Target)]
	if !ok {
		return nil, errors.Errorf("target stage %s could not be found", opt.Target)
	}
	return &state.state, nil
}

type dispatchState struct {
	state llb.State
	image ocispec.Image
	stage instructions.Stage
	base  *dispatchState
}

func dispatchEnv(d *dispatchState, c *instructions.EnvCommand) error {
	for _, e := range c.Env {
		d.state = d.state.AddEnv(e.Key, e.Value)
		// addEnv(d.image, e.Key, e.Value)
	}
	return nil
}

func dispatchRun(d *dispatchState, c *instructions.RunCommand) error {
	var args []string = c.CmdLine
	if c.PrependShell {
		args = append(defaultShell(), strings.Join(args, " "))
	}
	d.state = d.state.Run(llb.Args(args)).Root()
	return nil
}

func dispatchWorkdir(d *dispatchState, c *instructions.WorkdirCommand) error {
	d.state = d.state.Dir(c.Path)
	wd := c.Path
	if !path.IsAbs(c.Path) {
		wd = path.Join("/", d.image.Config.WorkingDir, wd)
	}
	d.image.Config.WorkingDir = wd
	return nil
}

func dispatchCopy(d *dispatchState, c instructions.SourcesAndDest, sourceState llb.State) error {
	// TODO: this should use CopyOp instead. Current implementation is inefficient and doesn't match Dockerfile path suffixes rules
	img := llb.Image("docker.io/library/alpine@sha256:1072e499f3f655a032e88542330cf75b02e7bdf673278f701d7ba61629ee3ebe")

	destDir := filepath.Join("/dest", toWorkingDir(d.state, c.Dest())) // TODO: detect file source + no dest path case
	srcs := make([]string, 0, len(c.Sources()))
	for _, src := range c.Sources() {
		src = path.Join("/src", toWorkingDir(sourceState, src))
		if src == "/src" {
			src += "/."
		}
		srcs = append(srcs, src)
	}

	run := img.Run(llb.Shlexf("sh -c \"mkdir -p %s && cp -a %s %s\"", destDir, strings.Join(srcs, " "), destDir))
	run.AddMount("/src", sourceState, llb.Readonly)
	d.state = run.AddMount("/dest", d.state)
	return nil
}

func toWorkingDir(s llb.State, p string) string {
	if path.IsAbs(p) {
		return p
	}
	return path.Join(s.GetDir(), p)
}

func dispatchMaintainer(d *dispatchState, c instructions.MaintainerCommand) error {
	d.image.Author = c.Maintainer
	return nil
}

func dispatchLabel(d *dispatchState, c instructions.LabelCommand) error {
	if d.image.Config.Labels == nil {
		d.image.Config.Labels = make(map[string]string)
	}
	for _, v := range c.Labels {
		d.image.Config.Labels[v.Key] = v.Value
	}
	return nil
}

// func dispatchOnbuild(d *dispatchState, c *instructions.OnbuildCommand) error {
// 	d.image.Config.OnBuild = append(d.image.Config.OnBuild, c.Expression)
// 	return nil
// }

func dispatchCmd(d *dispatchState, c *instructions.CmdCommand) error {
	var args []string = c.CmdLine
	if c.PrependShell {
		args = append(defaultShell(), strings.Join(args, " "))
	}
	d.image.Config.Cmd = args
	// d.image.Config.ArgsEscaped = true
	return nil
}
