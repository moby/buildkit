package ops

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/containerd/containerd/platforms"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"go.opentelemetry.io/otel/trace"

	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/executor"
	"github.com/moby/buildkit/frontend/gateway"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/localhost"
	"github.com/moby/buildkit/session/secrets"
	"github.com/moby/buildkit/snapshot"
	"github.com/moby/buildkit/solver"
	"github.com/moby/buildkit/solver/llbsolver/errdefs"
	"github.com/moby/buildkit/solver/llbsolver/mounts"
	"github.com/moby/buildkit/solver/llbsolver/ops/opsutils"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/progress/logs"
	"github.com/moby/buildkit/util/semutil"
	utilsystem "github.com/moby/buildkit/util/system"
	"github.com/moby/buildkit/worker"
)

const execCacheType = "buildkit.exec.v0"

type ExecOp struct {
	op          *pb.ExecOp
	cm          cache.Manager
	mm          *mounts.MountManager
	sm          *session.Manager
	exec        executor.Executor
	w           worker.Worker
	platform    *pb.Platform
	numInputs   int
	parallelism *semutil.Weighted
}

var _ solver.Op = &ExecOp{}

func NewExecOp(v solver.Vertex, op *pb.Op_Exec, platform *pb.Platform, cm cache.Manager, parallelism *semutil.Weighted, sm *session.Manager, exec executor.Executor, w worker.Worker) (*ExecOp, error) {
	if err := opsutils.Validate(&pb.Op{Op: op}); err != nil {
		return nil, err
	}
	name := fmt.Sprintf("exec %s", strings.Join(op.Exec.Meta.Args, " "))
	return &ExecOp{
		op:          op.Exec,
		mm:          mounts.NewMountManager(name, cm, sm),
		cm:          cm,
		sm:          sm,
		exec:        exec,
		numInputs:   len(v.Inputs()),
		w:           w,
		platform:    platform,
		parallelism: parallelism,
	}, nil
}

func (e *ExecOp) Proto() *pb.ExecOp {
	return e.op
}

func cloneExecOp(old *pb.ExecOp) pb.ExecOp {
	n := *old
	meta := *n.Meta
	meta.ExtraHosts = nil
	for i := range n.Meta.ExtraHosts {
		h := *n.Meta.ExtraHosts[i]
		meta.ExtraHosts = append(meta.ExtraHosts, &h)
	}
	n.Meta = &meta
	n.Mounts = nil
	for i := range old.Mounts {
		m := *old.Mounts[i]
		n.Mounts = append(n.Mounts, &m)
	}
	return n
}

func (e *ExecOp) CacheMap(ctx context.Context, g session.Group, index int) (*solver.CacheMap, bool, error) {
	op := cloneExecOp(e.op)
	for i := range op.Meta.ExtraHosts {
		h := op.Meta.ExtraHosts[i]
		h.IP = ""
		op.Meta.ExtraHosts[i] = h
	}
	for i := range op.Mounts {
		op.Mounts[i].Selector = ""
	}
	op.Meta.ProxyEnv = nil

	p := platforms.DefaultSpec()
	if e.platform != nil {
		p = ocispecs.Platform{
			OS:           e.platform.OS,
			Architecture: e.platform.Architecture,
			Variant:      e.platform.Variant,
		}
	}

	// Special case for cache compatibility with buggy versions that wrongly
	// excluded Exec.Mounts: for the default case of one root mount (i.e. RUN
	// inside a Dockerfile), do not include the mount when generating the cache
	// map.
	if len(op.Mounts) == 1 &&
		op.Mounts[0].Dest == "/" &&
		op.Mounts[0].Selector == "" &&
		!op.Mounts[0].Readonly &&
		op.Mounts[0].MountType == pb.MountType_BIND &&
		op.Mounts[0].CacheOpt == nil &&
		op.Mounts[0].SSHOpt == nil &&
		op.Mounts[0].SecretOpt == nil &&
		op.Mounts[0].ResultID == "" {
		op.Mounts = nil
	}

	dt, err := json.Marshal(struct {
		Type    string
		Exec    *pb.ExecOp
		OS      string
		Arch    string
		Variant string `json:",omitempty"`
	}{
		Type:    execCacheType,
		Exec:    &op,
		OS:      p.OS,
		Arch:    p.Architecture,
		Variant: p.Variant,
	})
	if err != nil {
		return nil, false, err
	}

	cm := &solver.CacheMap{
		Digest: digest.FromBytes(dt),
		Deps: make([]struct {
			Selector          digest.Digest
			ComputeDigestFunc solver.ResultBasedCacheFunc
			PreprocessFunc    solver.PreprocessFunc
		}, e.numInputs),
	}

	deps, err := e.getMountDeps()
	if err != nil {
		return nil, false, err
	}

	for i, dep := range deps {
		if len(dep.Selectors) != 0 {
			dgsts := make([][]byte, 0, len(dep.Selectors))
			for _, p := range dep.Selectors {
				dgsts = append(dgsts, []byte(p))
			}
			cm.Deps[i].Selector = digest.FromBytes(bytes.Join(dgsts, []byte{0}))
		}
		if !dep.NoContentBasedHash {
			cm.Deps[i].ComputeDigestFunc = opsutils.NewContentHashFunc(toSelectors(dedupePaths(dep.Selectors)))
		}
		cm.Deps[i].PreprocessFunc = unlazyResultFunc
	}

	return cm, true, nil
}

func dedupePaths(inp []string) []string {
	old := make(map[string]struct{}, len(inp))
	for _, p := range inp {
		old[p] = struct{}{}
	}
	paths := make([]string, 0, len(old))
	for p1 := range old {
		var skip bool
		for p2 := range old {
			if p1 != p2 && strings.HasPrefix(p1, p2+"/") {
				skip = true
				break
			}
		}
		if !skip {
			paths = append(paths, p1)
		}
	}
	sort.Slice(paths, func(i, j int) bool {
		return paths[i] < paths[j]
	})
	return paths
}

func toSelectors(p []string) []opsutils.Selector {
	sel := make([]opsutils.Selector, 0, len(p))
	for _, p := range p {
		sel = append(sel, opsutils.Selector{Path: p, FollowLinks: true})
	}
	return sel
}

type dep struct {
	Selectors          []string
	NoContentBasedHash bool
}

func (e *ExecOp) getMountDeps() ([]dep, error) {
	deps := make([]dep, e.numInputs)
	for _, m := range e.op.Mounts {
		if m.Input == pb.Empty {
			continue
		}
		if int(m.Input) >= len(deps) {
			return nil, errors.Errorf("invalid mountinput %v", m)
		}

		sel := m.Selector
		if sel != "" {
			sel = path.Join("/", sel)
			deps[m.Input].Selectors = append(deps[m.Input].Selectors, sel)
		}

		if (!m.Readonly || m.Dest == pb.RootMount) && m.Output != -1 { // exclude read-only rootfs && read-write mounts
			deps[m.Input].NoContentBasedHash = true
		}
	}
	return deps, nil
}

func addDefaultEnvvar(env []string, k, v string) []string {
	for _, e := range env {
		if strings.HasPrefix(e, k+"=") {
			return env
		}
	}
	return append(env, k+"="+v)
}

func (e *ExecOp) Exec(ctx context.Context, g session.Group, inputs []solver.Result) (results []solver.Result, err error) {
	trace.SpanFromContext(ctx).AddEvent("ExecOp started")

	refs := make([]*worker.WorkerRef, len(inputs))
	for i, inp := range inputs {
		var ok bool
		refs[i], ok = inp.Sys().(*worker.WorkerRef)
		if !ok {
			return nil, errors.Errorf("invalid reference for exec %T", inp.Sys())
		}
	}

	p, err := gateway.PrepareMounts(ctx, e.mm, e.cm, g, e.op.Meta.Cwd, e.op.Mounts, refs, func(m *pb.Mount, ref cache.ImmutableRef) (cache.MutableRef, error) {
		desc := fmt.Sprintf("mount %s from exec %s", m.Dest, strings.Join(e.op.Meta.Args, " "))
		return e.cm.New(ctx, ref, g, cache.WithDescription(desc))
	})
	defer func() {
		if err != nil {
			execInputs := make([]solver.Result, len(e.op.Mounts))
			for i, m := range e.op.Mounts {
				if m.Input == -1 {
					continue
				}
				execInputs[i] = inputs[m.Input].Clone()
			}
			execMounts := make([]solver.Result, len(e.op.Mounts))
			copy(execMounts, execInputs)
			for i, res := range results {
				execMounts[p.OutputRefs[i].MountIndex] = res
			}
			for _, active := range p.Actives {
				if active.NoCommit {
					active.Ref.Release(context.TODO())
				} else {
					ref, cerr := active.Ref.Commit(ctx)
					if cerr != nil {
						err = errors.Wrapf(err, "error committing %s: %s", active.Ref.ID(), cerr)
						continue
					}
					execMounts[active.MountIndex] = worker.NewWorkerRefResult(ref, e.w)
				}
			}
			err = errdefs.WithExecError(err, execInputs, execMounts)
		} else {
			// Only release actives if err is nil.
			for i := len(p.Actives) - 1; i >= 0; i-- { // call in LIFO order
				p.Actives[i].Ref.Release(context.TODO())
			}
		}
		for _, o := range p.OutputRefs {
			if o.Ref != nil {
				o.Ref.Release(context.TODO())
			}
		}
	}()
	if err != nil {
		return nil, err
	}

	extraHosts, err := gateway.ParseExtraHosts(e.op.Meta.ExtraHosts)
	if err != nil {
		return nil, err
	}

	emu, err := getEmulator(ctx, e.platform, e.cm.IdentityMapping())
	if err != nil {
		return nil, err
	}
	if emu != nil {
		e.op.Meta.Args = append([]string{qemuMountName}, e.op.Meta.Args...)

		p.Mounts = append(p.Mounts, executor.Mount{
			Readonly: true,
			Src:      emu,
			Dest:     qemuMountName,
		})
	}

	meta := executor.Meta{
		Args:                      e.op.Meta.Args,
		Env:                       e.op.Meta.Env,
		Cwd:                       e.op.Meta.Cwd,
		User:                      e.op.Meta.User,
		Hostname:                  e.op.Meta.Hostname,
		ReadonlyRootFS:            p.ReadonlyRootFS,
		ExtraHosts:                extraHosts,
		Ulimit:                    e.op.Meta.Ulimit,
		CgroupParent:              e.op.Meta.CgroupParent,
		NetMode:                   e.op.Network,
		SecurityMode:              e.op.Security,
		RemoveMountStubsRecursive: e.op.Meta.RemoveMountStubsRecursive,
	}

	if e.op.Meta.ProxyEnv != nil {
		meta.Env = append(meta.Env, proxyEnvList(e.op.Meta.ProxyEnv)...)
	}
	var currentOS string
	if e.platform != nil {
		currentOS = e.platform.OS
	}
	meta.Env = addDefaultEnvvar(meta.Env, "PATH", utilsystem.DefaultPathEnv(currentOS))

	secretEnv, err := e.loadSecretEnv(ctx, g)
	if err != nil {
		return nil, err
	}
	meta.Env = append(meta.Env, secretEnv...)

	stdout, stderr, flush := logs.NewLogStreams(ctx, os.Getenv("BUILDKIT_DEBUG_EXEC_OUTPUT") == "1")
	defer stdout.Close()
	defer stderr.Close()
	defer func() {
		if err != nil {
			flush()
		}
	}()

	isLocal, err := e.doFromLocalHack(ctx, p.Root, p.Mounts, g, meta, stdout, stderr)
	if err != nil {
		return nil, err
	}

	var execErr error
	if !isLocal {
		execErr = e.exec.Run(ctx, "", p.Root, p.Mounts, executor.ProcessInfo{
			Meta:   meta,
			Stdin:  nil,
			Stdout: stdout,
			Stderr: stderr,
		}, nil)
	}

	for i, out := range p.OutputRefs {
		if mutable, ok := out.Ref.(cache.MutableRef); ok {
			ref, err := mutable.Commit(ctx)
			if err != nil {
				return nil, errors.Wrapf(err, "error committing %s", mutable.ID())
			}
			results = append(results, worker.NewWorkerRefResult(ref, e.w))
		} else {
			results = append(results, worker.NewWorkerRefResult(out.Ref.(cache.ImmutableRef), e.w))
		}
		// Prevent the result from being released.
		p.OutputRefs[i].Ref = nil
	}
	return results, errors.Wrapf(execErr, "process %q did not complete successfully", strings.Join(e.op.Meta.Args, " "))
}

// earthly-specific
func (e *ExecOp) doFromLocalHack(ctx context.Context, root executor.Mount, mounts []executor.Mount, g session.Group, meta executor.Meta, stdout, stderr io.WriteCloser) (bool, error) {
	var cmd string
	if len(meta.Args) > 0 {
		cmd = meta.Args[0]
	}
	switch cmd {
	case localhost.CopyFileMagicStr:
		return true, e.copyLocally(ctx, root, g, meta, stdout, stderr)
	case localhost.RunOnLocalHostMagicStr:
		return true, e.execLocally(ctx, root, g, meta, stdout, stderr)
	case localhost.SendFileMagicStr:
		return true, e.sendLocally(ctx, root, mounts, g, meta, stdout, stderr)
	default:
		return false, nil
	}
}

func (e *ExecOp) copyLocally(ctx context.Context, root executor.Mount, g session.Group, meta executor.Meta, stdout, stderr io.WriteCloser) error {
	if len(meta.Args) != 3 {
		return fmt.Errorf("CopyFileMagicStr takes exactly 2 args")
	}
	if meta.Args[0] != localhost.CopyFileMagicStr {
		panic("arg[0] must be CopyFileMagicStr; this should not have happened")
	}
	src := meta.Args[1]
	if !strings.HasPrefix(src, "/") && meta.Cwd != "" {
		src = filepath.Join(meta.Cwd, src)
	}
	src = filepath.Clean(src)
	dst := meta.Args[2]

	if src == "/" {
		return fmt.Errorf("copyLocally does not support copying the entire root filesystem")
	}

	if strings.HasSuffix(dst, ".") || strings.HasSuffix(dst, "/") {
		dst = filepath.Join(dst, filepath.Base(src))
	}

	return e.sm.Any(ctx, g, func(ctx context.Context, _ string, caller session.Caller) error {
		mountable, err := root.Src.Mount(ctx, false)
		if err != nil {
			return err
		}

		rootMounts, release, err := mountable.Mount()
		if err != nil {
			return err
		}
		if release != nil {
			defer release()
		}

		lm := snapshot.LocalMounterWithMounts(rootMounts)
		rootfsPath, err := lm.Mount()
		if err != nil {
			return err
		}
		defer lm.Unmount()

		finalDest := rootfsPath + "/" + dst
		err = localhost.LocalhostGet(ctx, caller, src, finalDest, mountable)
		if err != nil {
			return err
		}
		return nil
	})
}

var errSendFileMagicStrMissingArgs = fmt.Errorf("SendFileMagicStr args missing; should be SendFileMagicStr [--dir] [--] <src> [<src> ...] <dst>")

func (e *ExecOp) sendLocally(ctx context.Context, root executor.Mount, mounts []executor.Mount, g session.Group, meta executor.Meta, stdout, stderr io.WriteCloser) error {
	i := 0
	nArgs := len(meta.Args)

	if i >= nArgs || meta.Args[i] != localhost.SendFileMagicStr {
		return errSendFileMagicStrMissingArgs
	}
	i++

	// check for --dir
	copyDir := false
	if i >= nArgs {
		return errSendFileMagicStrMissingArgs
	}
	if meta.Args[i] == "--dir" {
		copyDir = true
		i++
	}

	// check for -
	if i >= nArgs {
		return errSendFileMagicStrMissingArgs
	}
	if meta.Args[i] == "-" {
		i++
	}

	dstIndex := len(meta.Args) - 1
	numFiles := dstIndex - i
	if numFiles <= 0 {
		return fmt.Errorf("SendFileMagicStr args missing; should be SendFileMagicStr [--dir] [--] <src> [<src> ...] <dst>")
	}
	files := meta.Args[i:dstIndex]
	dst := meta.Args[dstIndex]

	if len(mounts) != 1 {
		return fmt.Errorf("SendFileMagicStr must be given a mount with the artifacts to copy from")
	}

	return e.sm.Any(ctx, g, func(ctx context.Context, _ string, caller session.Caller) error {
		mnt := mounts[0]

		mountable2, err := mnt.Src.Mount(ctx, false)
		if err != nil {
			return err
		}

		mounts, release, err := mountable2.Mount()
		if err != nil {
			return err
		}
		if release != nil {
			defer release()
		}

		lm := snapshot.LocalMounterWithMounts(mounts)
		hackfsPath, err := lm.Mount()
		if err != nil {
			return err
		}
		defer lm.Unmount()

		for _, f := range files {
			finalSrc := hackfsPath + "/" + f
			var finalDst string
			if dst == "." || strings.HasSuffix(dst, "/") || strings.HasSuffix(dst, "/.") || copyDir {
				finalDst = path.Join(dst, path.Base(f))
			} else {
				finalDst = dst
			}
			if !strings.HasPrefix(dst, "/") && meta.Cwd != "" {
				finalDst = path.Join(meta.Cwd, finalDst)
			}
			err = localhost.LocalhostPut(ctx, caller, finalSrc, finalDst)
			if err != nil {
				return errors.Wrap(err, "error calling LocalhostExec")
			}
		}
		return nil
	})
}

func (e *ExecOp) execLocally(ctx context.Context, root executor.Mount, g session.Group, meta executor.Meta, stdout, stderr io.WriteCloser) error {
	if len(meta.Args) == 0 || meta.Args[0] != localhost.RunOnLocalHostMagicStr {
		panic("first arg should be RunOnLocalHostMagicStr; this should not happen")
	}
	args := meta.Args[1:] // remove magic uuid from command prefix; the rest that follows is the actual command to run
	cwd := meta.Cwd

	return e.sm.Any(ctx, g, func(ctx context.Context, _ string, caller session.Caller) error {
		err := localhost.LocalhostExec(ctx, caller, args, cwd, stdout, stderr)
		if err != nil {
			return errors.Wrap(err, "error calling LocalhostExec")
		}
		return nil
	})
}

func proxyEnvList(p *pb.ProxyEnv) []string {
	out := []string{}
	if v := p.HttpProxy; v != "" {
		out = append(out, "HTTP_PROXY="+v, "http_proxy="+v)
	}
	if v := p.HttpsProxy; v != "" {
		out = append(out, "HTTPS_PROXY="+v, "https_proxy="+v)
	}
	if v := p.FtpProxy; v != "" {
		out = append(out, "FTP_PROXY="+v, "ftp_proxy="+v)
	}
	if v := p.NoProxy; v != "" {
		out = append(out, "NO_PROXY="+v, "no_proxy="+v)
	}
	if v := p.AllProxy; v != "" {
		out = append(out, "ALL_PROXY="+v, "all_proxy="+v)
	}
	return out
}

func (e *ExecOp) Acquire(ctx context.Context) (solver.ReleaseFunc, error) {
	if e.parallelism == nil {
		return func() {}, nil
	}
	err := e.parallelism.Acquire(ctx, 1)
	if err != nil {
		return nil, err
	}
	return func() {
		e.parallelism.Release(1)
	}, nil
}

func (e *ExecOp) loadSecretEnv(ctx context.Context, g session.Group) ([]string, error) {
	secretenv := e.op.Secretenv
	if len(secretenv) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(secretenv))
	for _, sopt := range secretenv {
		id := sopt.ID
		if id == "" {
			return nil, errors.Errorf("secret ID missing for %q environment variable", sopt.Name)
		}
		var dt []byte
		var err error
		err = e.sm.Any(ctx, g, func(ctx context.Context, _ string, caller session.Caller) error {
			dt, err = secrets.GetSecret(ctx, caller, id)
			if err != nil {
				if errors.Is(err, secrets.ErrNotFound) && sopt.Optional {
					return nil
				}
				return err
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		out = append(out, fmt.Sprintf("%s=%s", sopt.Name, string(dt)))
	}
	return out, nil
}

func (e *ExecOp) IsProvenanceProvider() {
}
