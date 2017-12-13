package dockerfile2llb

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/docker/distribution/reference"
	"github.com/docker/docker/builder/dockerfile/instructions"
	"github.com/docker/docker/builder/dockerfile/parser"
	"github.com/docker/docker/pkg/signal"
	"github.com/docker/docker/pkg/urlutil"
	"github.com/docker/go-connections/nat"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/client/llb/imagemetaresolver"
	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"
)

const (
	emptyImageName   = "scratch"
	localNameContext = "context"
	historyComment   = "buildkit.dockerfile.v0"
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
		metaResolver = imagemetaresolver.Default()
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
			deps:  make(map[*dispatchState]struct{}),
		}
		if d, ok := dispatchStatesByName[st.BaseName]; ok {
			ds.base = d
		}
		allDispatchStates = append(allDispatchStates, ds)
		if st.Name != "" {
			dispatchStatesByName[strings.ToLower(st.Name)] = ds
		}
	}

	var target *dispatchState
	if opt.Target == "" {
		target = allDispatchStates[len(allDispatchStates)-1]
	} else {
		var ok bool
		target, ok = dispatchStatesByName[strings.ToLower(opt.Target)]
		if !ok {
			return nil, nil, errors.Errorf("target stage %s could not be found", opt.Target)
		}
	}

	// fill dependencies to stages so unreachable ones can avoid loading image configs
	for _, d := range allDispatchStates {
		for _, cmd := range d.stage.Commands {
			if c, ok := cmd.(*instructions.CopyCommand); ok {
				if c.From != "" {
					index, err := strconv.Atoi(c.From)
					if err != nil {
						stn, ok := dispatchStatesByName[strings.ToLower(c.From)]
						if !ok {
							return nil, nil, errors.Errorf("stage %s not found", c.From)
						}
						d.deps[stn] = struct{}{}
					} else {
						if index < 0 || index >= len(allDispatchStates) {
							return nil, nil, errors.Errorf("invalid stage index %d", index)
						}
						d.deps[allDispatchStates[index]] = struct{}{}
					}
				}
			}
		}
	}

	eg, ctx := errgroup.WithContext(ctx)
	for i, d := range allDispatchStates {
		// resolve image config for every stage
		if d.base == nil {
			if d.stage.BaseName == emptyImageName {
				d.state = llb.Scratch()
				d.image = emptyImage()
				continue
			}
			func(i int, d *dispatchState) {
				eg.Go(func() error {
					ref, err := reference.ParseNormalizedNamed(d.stage.BaseName)
					if err != nil {
						return err
					}
					d.stage.BaseName = reference.TagNameOnly(ref).String()
					if metaResolver != nil && isReachable(target, d) {
						dgst, dt, err := metaResolver.ResolveImageConfig(ctx, d.stage.BaseName)
						if err == nil { // handle the error while builder is actually running
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
					d.state = llb.Image(d.stage.BaseName, dfCmd(d.stage.SourceCode))
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
			if err := dispatchEnv(d, &instructions.EnvCommand{Env: []instructions.KeyValuePair{{Key: parts[0], Value: v}}}, false); err != nil {
				return nil, nil, err
			}
		}
		if d.image.Config.WorkingDir != "" {
			if err = dispatchWorkdir(d, &instructions.WorkdirCommand{Path: d.image.Config.WorkingDir}, false); err != nil {
				return nil, nil, err
			}
		}
		if d.image.Config.User != "" {
			if err = dispatchUser(d, &instructions.UserCommand{User: d.image.Config.User}, false); err != nil {
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

	return &target.state, &target.image, nil
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
		err = dispatchEnv(d, c, true)
	case *instructions.RunCommand:
		err = dispatchRun(d, c)
	case *instructions.WorkdirCommand:
		err = dispatchWorkdir(d, c, true)
	case *instructions.AddCommand:
		err = dispatchCopy(d, c.SourcesAndDest, opt.buildContext, true, c)
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
		err = dispatchExpose(d, c, opt.shlex)
	case *instructions.UserCommand:
		err = dispatchUser(d, c, true)
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
		err = dispatchCopy(d, c.SourcesAndDest, l, false, c)
	default:
	}
	return err
}

type dispatchState struct {
	state     llb.State
	image     Image
	stage     instructions.Stage
	base      *dispatchState
	deps      map[*dispatchState]struct{}
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

func dispatchEnv(d *dispatchState, c *instructions.EnvCommand, commit bool) error {
	commitMessage := bytes.NewBufferString("ENV")
	for _, e := range c.Env {
		commitMessage.WriteString(" " + e.String())
		d.state = d.state.AddEnv(e.Key, e.Value)
		d.image.Config.Env = addEnv(d.image.Config.Env, e.Key, e.Value, true)
	}
	if commit {
		return commitToHistory(&d.image, commitMessage.String(), false, nil)
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
	opt = append(opt, dfCmd(c))
	d.state = d.state.Run(opt...).Root()
	return commitToHistory(&d.image, "RUN "+runCommandString(args, d.buildArgs), true, &d.state)
}

func dispatchWorkdir(d *dispatchState, c *instructions.WorkdirCommand, commit bool) error {
	d.state = d.state.Dir(c.Path)
	wd := c.Path
	if !path.IsAbs(c.Path) {
		wd = path.Join("/", d.image.Config.WorkingDir, wd)
	}
	d.image.Config.WorkingDir = wd
	if commit {
		return commitToHistory(&d.image, "WORKDIR "+wd, false, nil)
	}
	return nil
}

func dispatchCopy(d *dispatchState, c instructions.SourcesAndDest, sourceState llb.State, isAddCommand bool, cmdToPrint interface{}) error {
	// TODO: this should use CopyOp instead. Current implementation is inefficient
	img := llb.Image("tonistiigi/copy@sha256:ca6620946b2db7ad92f80cc5834f5d0724e6ede1f00004c965f6ded61592553a")

	dest := path.Join("/dest", pathRelativeToWorkingDir(d.state, c.Dest()))
	if c.Dest() == "." || c.Dest()[len(c.Dest())-1] == filepath.Separator {
		dest += string(filepath.Separator)
	}
	args := []string{"copy"}
	if isAddCommand {
		args = append(args, "--unpack")
	}

	commitMessage := bytes.NewBufferString("")
	if isAddCommand {
		commitMessage.WriteString("ADD")
	} else {
		commitMessage.WriteString("COPY")
	}

	mounts := make([]llb.RunOption, 0, len(c.Sources()))
	for i, src := range c.Sources() {
		commitMessage.WriteString(" " + src)
		if isAddCommand && urlutil.IsURL(src) {
			u, err := url.Parse(src)
			f := "__unnamed__"
			if err == nil {
				if base := path.Base(u.Path); base != "." && base != "/" {
					f = base
				}
			}
			target := path.Join(fmt.Sprintf("/src-%d", i), f)
			args = append(args, target)
			mounts = append(mounts, llb.AddMount(target, llb.HTTP(src, llb.Filename(f), dfCmd(c)), llb.Readonly))
		} else {
			d, f := splitWildcards(src)
			if f == "" {
				f = path.Base(src)
			}
			target := path.Join(fmt.Sprintf("/src-%d", i), f)
			args = append(args, target)
			mounts = append(mounts, llb.AddMount(target, sourceState, llb.SourcePath(d), llb.Readonly))
		}
	}

	commitMessage.WriteString(" " + c.Dest())

	args = append(args, dest)
	run := img.Run(append([]llb.RunOption{llb.Args(args), dfCmd(cmdToPrint)}, mounts...)...)
	d.state = run.AddMount("/dest", d.state)

	return commitToHistory(&d.image, commitMessage.String(), true, &d.state)
}

func dispatchMaintainer(d *dispatchState, c instructions.MaintainerCommand) error {
	d.image.Author = c.Maintainer
	return commitToHistory(&d.image, fmt.Sprintf("MAINTAINER %v", c.Maintainer), false, nil)
}

func dispatchLabel(d *dispatchState, c *instructions.LabelCommand) error {
	commitMessage := bytes.NewBufferString("LABEL")
	if d.image.Config.Labels == nil {
		d.image.Config.Labels = make(map[string]string)
	}
	for _, v := range c.Labels {
		d.image.Config.Labels[v.Key] = v.Value
		commitMessage.WriteString(" " + v.String())
	}
	return commitToHistory(&d.image, commitMessage.String(), false, nil)
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
	return commitToHistory(&d.image, fmt.Sprintf("CMD %q", args), false, nil)
}

func dispatchEntrypoint(d *dispatchState, c *instructions.EntrypointCommand) error {
	var args []string = c.CmdLine
	if c.PrependShell {
		args = append(defaultShell(), strings.Join(args, " "))
	}
	d.image.Config.Entrypoint = args
	return commitToHistory(&d.image, fmt.Sprintf("ENTRYPOINT %q", args), false, nil)
}

func dispatchHealthcheck(d *dispatchState, c *instructions.HealthCheckCommand) error {
	d.image.Config.Healthcheck = &HealthConfig{
		Test:        c.Health.Test,
		Interval:    c.Health.Interval,
		Timeout:     c.Health.Timeout,
		StartPeriod: c.Health.StartPeriod,
		Retries:     c.Health.Retries,
	}
	return commitToHistory(&d.image, fmt.Sprintf("HEALTHCHECK %q", d.image.Config.Healthcheck), false, nil)
}

func dispatchExpose(d *dispatchState, c *instructions.ExposeCommand, shlex *ShellLex) error {
	ports := []string{}
	for _, p := range c.Ports {
		ps, err := shlex.ProcessWords(p, toEnvList(d.buildArgs, d.image.Config.Env))
		if err != nil {
			return err
		}
		ports = append(ports, ps...)
	}
	c.Ports = ports

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

	return commitToHistory(&d.image, fmt.Sprintf("EXPOSE %v", ps), false, nil)
}
func dispatchUser(d *dispatchState, c *instructions.UserCommand, commit bool) error {
	d.state = d.state.User(c.User)
	d.image.Config.User = c.User
	if commit {
		return commitToHistory(&d.image, fmt.Sprintf("USER %v", c.User), false, nil)
	}
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
	return commitToHistory(&d.image, fmt.Sprintf("VOLUME %v", c.Volumes), false, nil)
}

func dispatchStopSignal(d *dispatchState, c *instructions.StopSignalCommand) error {
	if _, err := signal.ParseSignal(c.Signal); err != nil {
		return err
	}
	d.image.Config.StopSignal = c.Signal
	return commitToHistory(&d.image, fmt.Sprintf("STOPSIGNAL %v", c.Signal), false, nil)
}

func dispatchShell(d *dispatchState, c *instructions.ShellCommand) error {
	d.image.Config.Shell = c.Shell
	return commitToHistory(&d.image, fmt.Sprintf("SHELL %v", c.Shell), false, nil)
}

func dispatchArg(d *dispatchState, c *instructions.ArgCommand, metaArgs []instructions.ArgCommand, buildArgValues map[string]string) error {
	commitStr := "ARG " + c.Key
	if c.Value != nil {
		commitStr += "=" + *c.Value
	}
	if c.Value == nil {
		for _, ma := range metaArgs {
			if ma.Key == c.Key {
				c.Value = ma.Value
			}
		}
	}

	d.buildArgs = append(d.buildArgs, setBuildArgValue(*c, buildArgValues))
	return commitToHistory(&d.image, commitStr, false, nil)
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

func dfCmd(cmd interface{}) llb.MetadataOpt {
	// TODO: add fmt.Stringer to instructions.Command to remove interface{}
	var cmdStr string
	if cmd, ok := cmd.(fmt.Stringer); ok {
		cmdStr = cmd.String()
	}
	if cmd, ok := cmd.(string); ok {
		cmdStr = cmd
	}
	return llb.WithDescription(map[string]string{
		"com.docker.dockerfile.v1.command": cmdStr,
	})
}

func runCommandString(args []string, buildArgs []instructions.ArgCommand) string {
	var tmpBuildEnv []string
	for _, arg := range buildArgs {
		tmpBuildEnv = append(tmpBuildEnv, arg.Key+"="+getArgValue(arg))
	}
	if len(tmpBuildEnv) > 0 {
		tmpBuildEnv = append([]string{fmt.Sprintf("|%d", len(tmpBuildEnv))}, tmpBuildEnv...)
	}

	return strings.Join(append(tmpBuildEnv, args...), " ")
}

func commitToHistory(img *Image, msg string, withLayer bool, st *llb.State) error {
	if st != nil {
		def, err := st.Marshal()
		if err != nil {
			return err
		}
		msg += " # buildkit:" + digest.FromBytes(def.Def[len(def.Def)-1]).String()
	}

	tm := time.Now().UTC()
	img.History = append(img.History, ocispec.History{
		Created:    &tm,
		CreatedBy:  msg,
		Comment:    historyComment,
		EmptyLayer: !withLayer,
	})
	return nil
}

func isReachable(from, to *dispatchState) (ret bool) {
	if from == nil {
		return false
	}
	if from == to || isReachable(from.base, to) {
		return true
	}
	for d := range from.deps {
		if isReachable(d, to) {
			return true
		}
	}
	return false
}
