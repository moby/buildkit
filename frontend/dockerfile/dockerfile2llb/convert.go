package dockerfile2llb

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/docker/distribution/reference"
	"github.com/docker/docker/builder/dockerfile/instructions"
	"github.com/docker/docker/builder/dockerfile/parser"
	"github.com/docker/docker/builder/dockerfile/shell"
	"github.com/docker/docker/pkg/signal"
	"github.com/docker/go-connections/nat"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/client/llb/imagemetaresolver"
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
	Labels       map[string]string
	SessionID    string
	BuildContext *llb.State
	Excludes     []string
	// IgnoreCache contains names of the stages that should not use build cache.
	// Empty slice means ignore cache for all stages. Nil doesn't disable cache.
	IgnoreCache []string
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

	shlex := shell.NewLex(dockerfile.EscapeToken)

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
			stage:    st,
			deps:     make(map[*dispatchState]struct{}),
			ctxPaths: make(map[string]struct{}),
		}
		if d, ok := dispatchStatesByName[st.BaseName]; ok {
			ds.base = d
		}
		allDispatchStates = append(allDispatchStates, ds)
		if st.Name != "" {
			dispatchStatesByName[strings.ToLower(st.Name)] = ds
		}
		if opt.IgnoreCache != nil {
			if len(opt.IgnoreCache) == 0 {
				ds.ignoreCache = true
			} else if st.Name != "" {
				for _, n := range opt.IgnoreCache {
					if strings.EqualFold(n, st.Name) {
						ds.ignoreCache = true
					}
				}
			}
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
		d.commands = make([]command, len(d.stage.Commands))
		for i, cmd := range d.stage.Commands {
			newCmd, created, err := toCommand(cmd, dispatchStatesByName, allDispatchStates)
			if err != nil {
				return nil, nil, err
			}
			d.commands[i] = newCmd
			if newCmd.copySource != nil {
				d.deps[newCmd.copySource] = struct{}{}
				if created {
					allDispatchStates = append(allDispatchStates, newCmd.copySource)
				}
			}
		}
	}

	eg, ctx := errgroup.WithContext(ctx)
	for i, d := range allDispatchStates {
		reachable := isReachable(target, d)
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
					if metaResolver != nil && reachable {
						dgst, dt, err := metaResolver.ResolveImageConfig(ctx, d.stage.BaseName)
						if err == nil { // handle the error while builder is actually running
							var img Image
							if err := json.Unmarshal(dt, &img); err != nil {
								return err
							}
							img.Created = nil
							d.image = img
							if dgst != "" {
								ref, err = reference.WithDigest(ref, dgst)
								if err != nil {
									return err
								}
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

	buildContext := &mutableOutput{}
	ctxPaths := map[string]struct{}{}

	for _, d := range allDispatchStates {
		if !isReachable(target, d) {
			continue
		}
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
			buildContext:         llb.NewState(buildContext),
		}

		if err = dispatchOnBuild(d, d.image.Config.OnBuild, opt); err != nil {
			return nil, nil, err
		}

		for _, cmd := range d.commands {
			if err := dispatch(d, cmd, opt); err != nil {
				return nil, nil, err
			}
		}

		for p := range d.ctxPaths {
			ctxPaths[p] = struct{}{}
		}
	}

	for k, v := range opt.Labels {
		target.image.Config.Labels[k] = v
	}

	opts := []llb.LocalOption{
		llb.SessionID(opt.SessionID),
		llb.ExcludePatterns(opt.Excludes),
		llb.SharedKeyHint(localNameContext),
	}
	if includePatterns := normalizeContextPaths(ctxPaths); includePatterns != nil {
		opts = append(opts, llb.IncludePatterns(includePatterns))
	}
	bc := llb.Local(localNameContext, opts...)
	if opt.BuildContext != nil {
		bc = *opt.BuildContext
	}
	buildContext.Output = bc.Output()

	return &target.state, &target.image, nil
}

func toCommand(ic instructions.Command, dispatchStatesByName map[string]*dispatchState, allDispatchStates []*dispatchState) (command, bool, error) {
	cmd := command{Command: ic}
	created := false
	if c, ok := ic.(*instructions.CopyCommand); ok {
		if c.From != "" {
			var stn *dispatchState
			index, err := strconv.Atoi(c.From)
			if err != nil {
				stn, ok = dispatchStatesByName[strings.ToLower(c.From)]
				if !ok {
					stn = &dispatchState{
						stage: instructions.Stage{BaseName: c.From},
						deps:  make(map[*dispatchState]struct{}),
					}
					created = true
				}
			} else {
				if index < 0 || index >= len(allDispatchStates) {
					return command{}, false, errors.Errorf("invalid stage index %d", index)
				}
				stn = allDispatchStates[index]
			}
			cmd.copySource = stn
		}
	}
	return cmd, created, nil
}

type dispatchOpt struct {
	allDispatchStates    []*dispatchState
	dispatchStatesByName map[string]*dispatchState
	metaArgs             []instructions.ArgCommand
	buildArgValues       map[string]string
	shlex                *shell.Lex
	sessionID            string
	buildContext         llb.State
}

func dispatch(d *dispatchState, cmd command, opt dispatchOpt) error {
	if ex, ok := cmd.Command.(instructions.SupportsSingleWordExpansion); ok {
		err := ex.Expand(func(word string) (string, error) {
			return opt.shlex.ProcessWord(word, toEnvList(d.buildArgs, d.image.Config.Env))
		})
		if err != nil {
			return err
		}
	}

	var err error
	switch c := cmd.Command.(type) {
	case *instructions.MaintainerCommand:
		err = dispatchMaintainer(d, c)
	case *instructions.EnvCommand:
		err = dispatchEnv(d, c, true)
	case *instructions.RunCommand:
		err = dispatchRun(d, c)
	case *instructions.WorkdirCommand:
		err = dispatchWorkdir(d, c, true)
	case *instructions.AddCommand:
		err = dispatchCopy(d, c.SourcesAndDest, opt.buildContext, true, c, "")
		if err == nil {
			for _, src := range c.Sources() {
				d.ctxPaths[path.Join("/", filepath.ToSlash(src))] = struct{}{}
			}
		}
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
		if cmd.copySource != nil {
			l = cmd.copySource.state
		}
		err = dispatchCopy(d, c.SourcesAndDest, l, false, c, c.Chown)
		if err == nil && cmd.copySource == nil {
			for _, src := range c.Sources() {
				d.ctxPaths[path.Join("/", filepath.ToSlash(src))] = struct{}{}
			}
		}
	default:
	}
	return err
}

type dispatchState struct {
	state       llb.State
	image       Image
	stage       instructions.Stage
	base        *dispatchState
	deps        map[*dispatchState]struct{}
	buildArgs   []instructions.ArgCommand
	commands    []command
	ctxPaths    map[string]struct{}
	ignoreCache bool
}

type command struct {
	instructions.Command
	copySource *dispatchState
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
		ic, err := instructions.ParseCommand(ast.AST.Children[0])
		if err != nil {
			return err
		}
		cmd, _, err := toCommand(ic, opt.dispatchStatesByName, opt.allDispatchStates)
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
	if d.ignoreCache {
		opt = append(opt, llb.IgnoreCache)
	}
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

func dispatchCopy(d *dispatchState, c instructions.SourcesAndDest, sourceState llb.State, isAddCommand bool, cmdToPrint interface{}, chown string) error {
	// TODO: this should use CopyOp instead. Current implementation is inefficient
	img := llb.Image("tonistiigi/copy@sha256:476e0a67a1e4650c6adaf213269a2913deb7c52cbc77f954026f769d51e1a14e")

	dest := path.Join("/dest", pathRelativeToWorkingDir(d.state, c.Dest()))
	if c.Dest() == "." || c.Dest()[len(c.Dest())-1] == filepath.Separator {
		dest += string(filepath.Separator)
	}
	args := []string{"copy"}
	if isAddCommand {
		args = append(args, "--unpack")
	}

	mounts := make([]llb.RunOption, 0, len(c.Sources()))
	if chown != "" {
		args = append(args, fmt.Sprintf("--chown=%s", chown))
		_, _, err := parseUser(chown)
		if err != nil {
			mounts = append(mounts, llb.AddMount("/etc/passwd", d.state, llb.SourcePath("/etc/passwd"), llb.Readonly))
			mounts = append(mounts, llb.AddMount("/etc/group", d.state, llb.SourcePath("/etc/group"), llb.Readonly))
		}
	}

	commitMessage := bytes.NewBufferString("")
	if isAddCommand {
		commitMessage.WriteString("ADD")
	} else {
		commitMessage.WriteString("COPY")
	}

	for i, src := range c.Sources() {
		commitMessage.WriteString(" " + src)
		if isAddCommand && (strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://")) {
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
			targetCmd := fmt.Sprintf("/src-%d", i)
			targetMount := targetCmd
			if f == "" {
				f = path.Base(src)
				targetMount = path.Join(targetMount, f)
			}
			targetCmd = path.Join(targetCmd, f)
			args = append(args, targetCmd)
			mounts = append(mounts, llb.AddMount(targetMount, sourceState, llb.SourcePath(d), llb.Readonly))
		}
	}

	commitMessage.WriteString(" " + c.Dest())

	args = append(args, dest)
	run := img.Run(append([]llb.RunOption{llb.Args(args), llb.ReadonlyRootFS(), dfCmd(cmdToPrint)}, mounts...)...)
	d.state = run.AddMount("/dest", d.state)

	return commitToHistory(&d.image, commitMessage.String(), true, &d.state)
}

func dispatchMaintainer(d *dispatchState, c *instructions.MaintainerCommand) error {
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

func dispatchExpose(d *dispatchState, c *instructions.ExposeCommand, shlex *shell.Lex) error {
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

	base := path.Base(name[:i])
	if name[:i] == "" || strings.HasSuffix(name[:i], string(filepath.Separator)) {
		base = ""
	}
	return path.Dir(name[:i]), base + name[i:]
}

func addEnv(env []string, k, v string, override bool) []string {
	gotOne := false
	for i, envVar := range env {
		envParts := strings.SplitN(envVar, "=", 2)
		compareFrom := envParts[0]
		if shell.EqualEnvKeys(compareFrom, k) {
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
		msg += " # buildkit"
	}

	img.History = append(img.History, ocispec.History{
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

func parseUser(str string) (uid uint32, gid uint32, err error) {
	if str == "" {
		return 0, 0, nil
	}
	parts := strings.SplitN(str, ":", 2)
	for i, v := range parts {
		switch i {
		case 0:
			uid, err = parseUID(v)
			if err != nil {
				return 0, 0, err
			}
			if len(parts) == 1 {
				gid = uid
			}
		case 1:
			gid, err = parseUID(v)
			if err != nil {
				return 0, 0, err
			}
		}
	}
	return
}

func parseUID(str string) (uint32, error) {
	if str == "root" {
		return 0, nil
	}
	uid, err := strconv.ParseUint(str, 10, 32)
	if err != nil {
		return 0, err
	}
	return uint32(uid), nil
}

func normalizeContextPaths(paths map[string]struct{}) []string {
	pathSlice := make([]string, 0, len(paths))
	for p := range paths {
		if p == "/" {
			return nil
		}
		pathSlice = append(pathSlice, p)
	}

	toDelete := map[string]struct{}{}
	for i := range pathSlice {
		for j := range pathSlice {
			if i == j {
				continue
			}
			if strings.HasPrefix(pathSlice[j], pathSlice[i]+"/") {
				delete(paths, pathSlice[j])
			}
		}
	}

	toSort := make([]string, 0, len(paths))
	for p := range paths {
		if _, ok := toDelete[p]; !ok {
			toSort = append(toSort, path.Join(".", p))
		}
	}
	sort.Slice(toSort, func(i, j int) bool {
		return toSort[i] < toSort[j]
	})
	return toSort
}

type mutableOutput struct {
	llb.Output
}
