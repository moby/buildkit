package dockerfile2llb

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/docker/distribution/reference"
	"github.com/docker/docker/builder/dockerfile/instructions"
	"github.com/docker/docker/builder/dockerfile/parser"
	"github.com/docker/docker/pkg/signal"
	"github.com/docker/go-connections/nat"
	"github.com/moby/buildkit/client/llb"
	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"
)

const (
	emptyImageName   = "scratch"
	localNameContext = "context"
)

type ConvertOpt struct {
	Target       string
	MetaResolver llb.ImageMetaResolver
	BuildArgs    map[string]string
	SessionID    string
	BuildContext *llb.State
}

func Dockerfile2LLB(ctx context.Context, dt []byte, opt ConvertOpt) (*llb.State, *Image, error) {
	if len(dt) == 0 {
		return nil, nil, errors.Errorf("the Dockerfile cannot be empty")
	}

	dockerfile, err := parser.Parse(bytes.NewReader(dt))
	if err != nil {
		return nil, nil, err
	}

	stages, metaArgs, err := instructions.Parse(dockerfile.AST)
	if err != nil {
		return nil, nil, err
	}

	for i := range metaArgs {
		metaArgs[i] = setBuildArgValue(metaArgs[i], opt.BuildArgs)
	}

	shlex := NewShellLex(dockerfile.EscapeToken)

	metaResolver := opt.MetaResolver
	if metaResolver == nil {
		metaResolver = llb.DefaultImageMetaResolver()
	}

	var allDispatchStates []*dispatchState
	dispatchStatesByName := map[string]*dispatchState{}

	// set base state for every image
	for _, st := range stages {
		name, err := shlex.ProcessWord(st.BaseName, toEnvList(metaArgs, nil))
		if err != nil {
			return nil, nil, err
		}
		st.BaseName = name

		ds := &dispatchState{
			stage: st,
		}
		if d, ok := dispatchStatesByName[st.BaseName]; ok {
			ds.base = d
		}
		allDispatchStates = append(allDispatchStates, ds)
		if st.Name != "" {
			dispatchStatesByName[strings.ToLower(st.Name)] = ds
		}
	}

	eg, ctx := errgroup.WithContext(ctx)
	for i, d := range allDispatchStates {
		// resolve image config for every stage
		if d.base == nil {
			if d.stage.BaseName == emptyImageName {
				d.state = llb.Scratch()
				continue
			}
			func(i int, d *dispatchState) {
				eg.Go(func() error {
					ref, err := reference.ParseNormalizedNamed(d.stage.BaseName)
					if err != nil {
						return err
					}
					d.stage.BaseName = reference.TagNameOnly(ref).String()
					if metaResolver != nil {
						dgst, dt, err := metaResolver.ResolveImageConfig(ctx, d.stage.BaseName)
						if err == nil { // handle the error while builder is actually running
							// TODO: detect unreachable stages so config is not pulled for them
							var img Image
							if err := json.Unmarshal(dt, &img); err != nil {
								return err
							}
							d.image = img
							ref, err := reference.WithDigest(ref, dgst)
							if err != nil {
								return err
							}
							d.stage.BaseName = ref.String()
							_ = ref
						}
					}
					d.state = llb.Image(d.stage.BaseName)
					return nil
				})
			}(i, d)
		}

	}

	if err := eg.Wait(); err != nil {
		return nil, nil, err
	}

	buildContext := llb.Local(localNameContext, llb.SessionID(opt.SessionID))
	if opt.BuildContext != nil {
		buildContext = *opt.BuildContext
	}

	for _, d := range allDispatchStates {
		if d.base != nil {
			d.state = d.base.state
			d.image = clone(d.base.image)
		}

		// initialize base metadata from image conf
		for _, env := range d.image.Config.Env {
			parts := strings.SplitN(env, "=", 2)
			v := ""
			if len(parts) > 1 {
				v = parts[1]
			}
			if err := dispatchEnv(d, &instructions.EnvCommand{Env: []instructions.KeyValuePair{{Key: parts[0], Value: v}}}); err != nil {
				return nil, nil, err
			}
		}
		if d.image.Config.WorkingDir != "" {
			if err = dispatchWorkdir(d, &instructions.WorkdirCommand{Path: d.image.Config.WorkingDir}); err != nil {
				return nil, nil, err
			}
		}
		if d.image.Config.User != "" {
			if err = dispatchUser(d, &instructions.UserCommand{User: d.image.Config.User}); err != nil {
				return nil, nil, err
			}
		}

		opt := dispatchOpt{
			allDispatchStates:    allDispatchStates,
			dispatchStatesByName: dispatchStatesByName,
			metaArgs:             metaArgs,
			buildArgValues:       opt.BuildArgs,
			shlex:                shlex,
			sessionID:            opt.SessionID,
			buildContext:         buildContext,
		}

		if err = dispatchOnBuild(d, d.image.Config.OnBuild, opt); err != nil {
			return nil, nil, err
		}

		for _, cmd := range d.stage.Commands {
			if err := dispatch(d, cmd, opt); err != nil {
				return nil, nil, err
			}
		}
	}

	if opt.Target == "" {
		return &allDispatchStates[len(allDispatchStates)-1].state, &allDispatchStates[len(allDispatchStates)-1].image, nil
	}

	state, ok := dispatchStatesByName[strings.ToLower(opt.Target)]
	if !ok {
		return nil, nil, errors.Errorf("target stage %s could not be found", opt.Target)
	}
	return &state.state, &state.image, nil
}

type dispatchOpt struct {
	allDispatchStates    []*dispatchState
	dispatchStatesByName map[string]*dispatchState
	metaArgs             []instructions.ArgCommand
	buildArgValues       map[string]string
	shlex                *ShellLex
	sessionID            string
	buildContext         llb.State
}

func dispatch(d *dispatchState, cmd instructions.Command, opt dispatchOpt) error {
	if ex, ok := cmd.(instructions.SupportsSingleWordExpansion); ok {
		err := ex.Expand(func(word string) (string, error) {
			return opt.shlex.ProcessWord(word, toEnvList(d.buildArgs, d.image.Config.Env))
		})
		if err != nil {
			return err
		}
	}

	var err error
	switch c := cmd.(type) {
	case *instructions.EnvCommand:
		err = dispatchEnv(d, c)
	case *instructions.RunCommand:
		err = dispatchRun(d, c)
	case *instructions.WorkdirCommand:
		err = dispatchWorkdir(d, c)
	case *instructions.AddCommand:
		err = dispatchCopy(d, c.SourcesAndDest, opt.buildContext)
	case *instructions.LabelCommand:
		err = dispatchLabel(d, c)
	case *instructions.OnbuildCommand:
		err = dispatchOnbuild(d, c)
	case *instructions.CmdCommand:
		err = dispatchCmd(d, c)
	case *instructions.EntrypointCommand:
		err = dispatchEntrypoint(d, c)
	case *instructions.HealthCheckCommand:
		err = dispatchHealthcheck(d, c)
	case *instructions.ExposeCommand:
		err = dispatchExpose(d, c)
	case *instructions.UserCommand:
		err = dispatchUser(d, c)
	case *instructions.VolumeCommand:
		err = dispatchVolume(d, c)
	case *instructions.StopSignalCommand:
		err = dispatchStopSignal(d, c)
	case *instructions.ShellCommand:
		err = dispatchShell(d, c)
	case *instructions.ArgCommand:
		err = dispatchArg(d, c, opt.metaArgs, opt.buildArgValues)
	case *instructions.CopyCommand:
		l := opt.buildContext
		if c.From != "" {
			index, err := strconv.Atoi(c.From)
			if err != nil {
				stn, ok := opt.dispatchStatesByName[strings.ToLower(c.From)]
				if !ok {
					return errors.Errorf("stage %s not found", c.From)
				}
				l = stn.state
			} else {
				if index >= len(opt.allDispatchStates) {
					return errors.Errorf("invalid stage index %d", index)
				}
				l = opt.allDispatchStates[index].state
			}
		}
		err = dispatchCopy(d, c.SourcesAndDest, l)
	default:
	}
	return err
}

type dispatchState struct {
	state llb.State
	image Image
	stage instructions.Stage
	base  *dispatchState

	buildArgs []instructions.ArgCommand
}

func dispatchOnBuild(d *dispatchState, triggers []string, opt dispatchOpt) error {
	for _, trigger := range triggers {
		ast, err := parser.Parse(strings.NewReader(trigger))
		if err != nil {
			return err
		}
		if len(ast.AST.Children) != 1 {
			return errors.New("onbuild trigger should be a single expression")
		}
		cmd, err := instructions.ParseCommand(ast.AST.Children[0])
		if err != nil {
			return err
		}
		if err := dispatch(d, cmd, opt); err != nil {
			return err
		}
	}
	return nil
}

func dispatchEnv(d *dispatchState, c *instructions.EnvCommand) error {
	for _, e := range c.Env {
		d.state = d.state.AddEnv(e.Key, e.Value)
		d.image.Config.Env = addEnv(d.image.Config.Env, e.Key, e.Value, true)
	}
	return nil
}

func dispatchRun(d *dispatchState, c *instructions.RunCommand) error {
	var args []string = c.CmdLine
	if c.PrependShell {
		args = append(defaultShell(), strings.Join(args, " "))
	} else if d.image.Config.Entrypoint != nil {
		args = append(d.image.Config.Entrypoint, args...)
	}
	opt := []llb.RunOption{llb.Args(args)}
	for _, arg := range d.buildArgs {
		opt = append(opt, llb.AddEnv(arg.Key, getArgValue(arg)))
	}
	d.state = d.state.Run(opt...).Root()
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
	img := llb.Image("tonistiigi/copy@sha256:260a4355be76e0609518ebd7c0e026831c80b8908d4afd3f8e8c942645b1e5cf")

	dest := path.Join("/dest", pathRelativeToWorkingDir(d.state, c.Dest()))
	if c.Dest() == "." || c.Dest()[len(c.Dest())-1] == filepath.Separator {
		dest += string(filepath.Separator)
	}
	args := []string{"copy"}
	mounts := make([]llb.RunOption, 0, len(c.Sources()))
	for i, src := range c.Sources() {
		d, f := splitWildcards(src)
		if f == "" {
			f = path.Base(src)
		}
		target := path.Join(fmt.Sprintf("/src-%d", i), f)
		args = append(args, target)
		mounts = append(mounts, llb.AddMount(target, sourceState, llb.SourcePath(d), llb.Readonly))
	}

	args = append(args, dest)
	run := img.Run(append([]llb.RunOption{llb.Args(args)}, mounts...)...)
	d.state = run.AddMount("/dest", d.state)
	return nil
}

func dispatchMaintainer(d *dispatchState, c instructions.MaintainerCommand) error {
	d.image.Author = c.Maintainer
	return nil
}

func dispatchLabel(d *dispatchState, c *instructions.LabelCommand) error {
	if d.image.Config.Labels == nil {
		d.image.Config.Labels = make(map[string]string)
	}
	for _, v := range c.Labels {
		d.image.Config.Labels[v.Key] = v.Value
	}
	return nil
}

func dispatchOnbuild(d *dispatchState, c *instructions.OnbuildCommand) error {
	d.image.Config.OnBuild = append(d.image.Config.OnBuild, c.Expression)
	return nil
}

func dispatchCmd(d *dispatchState, c *instructions.CmdCommand) error {
	var args []string = c.CmdLine
	if c.PrependShell {
		args = append(defaultShell(), strings.Join(args, " "))
	}
	d.image.Config.Cmd = args
	d.image.Config.ArgsEscaped = true
	return nil
}

func dispatchEntrypoint(d *dispatchState, c *instructions.EntrypointCommand) error {
	var args []string = c.CmdLine
	if c.PrependShell {
		args = append(defaultShell(), strings.Join(args, " "))
	}
	d.image.Config.Entrypoint = args
	return nil
}

func dispatchHealthcheck(d *dispatchState, c *instructions.HealthCheckCommand) error {
	d.image.Config.Healthcheck = &HealthConfig{
		Test:        c.Health.Test,
		Interval:    c.Health.Interval,
		Timeout:     c.Health.Timeout,
		StartPeriod: c.Health.StartPeriod,
		Retries:     c.Health.Retries,
	}
	return nil
}

func dispatchExpose(d *dispatchState, c *instructions.ExposeCommand) error {
	// TODO: custom multi word expansion
	ps, _, err := nat.ParsePortSpecs(c.Ports)
	if err != nil {
		return err
	}

	if d.image.Config.ExposedPorts == nil {
		d.image.Config.ExposedPorts = make(map[string]struct{})
	}
	for p := range ps {
		d.image.Config.ExposedPorts[string(p)] = struct{}{}
	}

	return nil
}
func dispatchUser(d *dispatchState, c *instructions.UserCommand) error {
	d.image.Config.User = c.User
	return nil
}

func dispatchVolume(d *dispatchState, c *instructions.VolumeCommand) error {
	if d.image.Config.Volumes == nil {
		d.image.Config.Volumes = map[string]struct{}{}
	}
	for _, v := range c.Volumes {
		if v == "" {
			return errors.New("VOLUME specified can not be an empty string")
		}
		d.image.Config.Volumes[v] = struct{}{}
	}
	return nil
}

func dispatchStopSignal(d *dispatchState, c *instructions.StopSignalCommand) error {
	if _, err := signal.ParseSignal(c.Signal); err != nil {
		return err
	}
	d.image.Config.StopSignal = c.Signal
	return nil
}

func dispatchShell(d *dispatchState, c *instructions.ShellCommand) error {
	d.image.Config.Shell = c.Shell
	return nil
}

func dispatchArg(d *dispatchState, c *instructions.ArgCommand, metaArgs []instructions.ArgCommand, buildArgValues map[string]string) error {
	if c.Value == nil {
		for _, ma := range metaArgs {
			if ma.Key == c.Key {
				c.Value = ma.Value
			}
		}
	}

	d.buildArgs = append(d.buildArgs, setBuildArgValue(*c, buildArgValues))
	return nil
}

func pathRelativeToWorkingDir(s llb.State, p string) string {
	if path.IsAbs(p) {
		return p
	}
	return path.Join(s.GetDir(), p)
}

func splitWildcards(name string) (string, string) {
	i := 0
	for ; i < len(name); i++ {
		ch := name[i]
		if ch == '\\' {
			i++
		} else if ch == '*' || ch == '?' || ch == '[' {
			break
		}
	}
	if i == len(name) {
		return name, ""
	}
	return path.Dir(name[:i]), path.Base(name[:i]) + name[i:]
}

func addEnv(env []string, k, v string, override bool) []string {
	gotOne := false
	for i, envVar := range env {
		envParts := strings.SplitN(envVar, "=", 2)
		compareFrom := envParts[0]
		if equalEnvKeys(compareFrom, k) {
			if override {
				env[i] = k + "=" + v
			}
			gotOne = true
			break
		}
	}
	if !gotOne {
		env = append(env, k+"="+v)
	}
	return env
}

func equalEnvKeys(from, to string) bool {
	return from == to
}

func setBuildArgValue(c instructions.ArgCommand, values map[string]string) instructions.ArgCommand {
	if v, ok := values[c.Key]; ok {
		c.Value = &v
	}
	return c
}

func toEnvList(args []instructions.ArgCommand, env []string) []string {
	for _, arg := range args {
		env = addEnv(env, arg.Key, getArgValue(arg), false)
	}
	return env
}

func getArgValue(arg instructions.ArgCommand) string {
	v := ""
	if arg.Value != nil {
		v = *arg.Value
	}
	return v
}
