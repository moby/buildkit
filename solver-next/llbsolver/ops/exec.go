package ops

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path"
	"runtime"
	"sort"
	"strings"

	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/executor"
	"github.com/moby/buildkit/solver-next"
	"github.com/moby/buildkit/solver-next/llbsolver"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/progress/logs"
	"github.com/moby/buildkit/worker"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
)

const execCacheType = "buildkit.exec.v0"

type execOp struct {
	op        *pb.ExecOp
	cm        cache.Manager
	exec      executor.Executor
	w         worker.Worker
	numInputs int
}

func NewExecOp(v solver.Vertex, op *pb.Op_Exec, cm cache.Manager, exec executor.Executor, w worker.Worker) (solver.Op, error) {
	return &execOp{
		op:        op.Exec,
		cm:        cm,
		exec:      exec,
		numInputs: len(v.Inputs()),
		w:         w,
	}, nil
}

func cloneExecOp(old *pb.ExecOp) pb.ExecOp {
	n := *old
	n.Mounts = nil
	for i := range n.Mounts {
		m := *n.Mounts[i]
		n.Mounts = append(n.Mounts, &m)
	}
	return n
}

func (e *execOp) CacheMap(ctx context.Context) (*solver.CacheMap, error) {
	op := cloneExecOp(e.op)
	for i := range op.Mounts {
		op.Mounts[i].Selector = ""
	}

	dt, err := json.Marshal(struct {
		Type string
		Exec *pb.ExecOp
		OS   string
		Arch string
	}{
		Type: execCacheType,
		Exec: &op,
		OS:   runtime.GOOS,
		Arch: runtime.GOARCH,
	})
	if err != nil {
		return nil, err
	}

	cm := &solver.CacheMap{
		Digest: digest.FromBytes(dt),
		Deps: make([]struct {
			Selector          digest.Digest
			ComputeDigestFunc solver.ResultBasedCacheFunc
		}, e.numInputs),
	}

	deps, err := e.getMountDeps()
	if err != nil {
		return nil, err
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
			cm.Deps[i].ComputeDigestFunc = llbsolver.NewContentHashFunc(dedupePaths(dep.Selectors))
		}
	}

	return cm, nil
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
			if p1 != p2 && strings.HasPrefix(p1, p2) {
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

type dep struct {
	Selectors          []string
	NoContentBasedHash bool
}

func (e *execOp) getMountDeps() ([]dep, error) {
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

		if !m.Readonly || m.Dest == pb.RootMount { // exclude read-only rootfs
			deps[m.Input].NoContentBasedHash = true
		}
	}
	return deps, nil
}

func (e *execOp) Exec(ctx context.Context, inputs []solver.Result) ([]solver.Result, error) {
	var mounts []executor.Mount
	var root cache.Mountable
	var readonlyRootFS bool

	var outputs []cache.Ref

	defer func() {
		for _, o := range outputs {
			if o != nil {
				go o.Release(context.TODO())
			}
		}
	}()

	for _, m := range e.op.Mounts {
		var mountable cache.Mountable
		var ref cache.ImmutableRef
		if m.Input != pb.Empty {
			if int(m.Input) > len(inputs) {
				return nil, errors.Errorf("missing input %d", m.Input)
			}
			inp := inputs[int(m.Input)]
			workerRef, ok := inp.Sys().(*worker.WorkerRef)
			if !ok {
				return nil, errors.Errorf("invalid reference for exec %T", inp.Sys())
			}
			ref = workerRef.ImmutableRef
			mountable = ref
		}

		activate := func(cache.ImmutableRef) (cache.MutableRef, error) {
			desc := fmt.Sprintf("mount %s from exec %s", m.Dest, strings.Join(e.op.Meta.Args, " "))
			return e.cm.New(ctx, ref, cache.WithDescription(desc)) // TODO: should be method `immutableRef.New() mutableRef`
		}

		if m.Output != pb.SkipOutput {
			if m.Readonly && ref != nil && m.Dest != pb.RootMount { // exclude read-only rootfs
				out, err := e.cm.Get(ctx, ref.ID()) // TODO: add dup to immutableRef
				if err != nil {
					return nil, err
				}
				outputs = append(outputs, out)
			} else {
				active, err := activate(ref)
				if err != nil {
					return nil, err
				}
				outputs = append(outputs, active)
				mountable = active
			}
		}

		if mountable == nil {
			return nil, errors.Errorf("mount %s has no input", m.Dest)
		}

		if m.Dest == pb.RootMount {
			root = mountable
			readonlyRootFS = m.Readonly
			if m.Output == pb.SkipOutput && readonlyRootFS {
				active, err := activate(ref)
				if err != nil {
					return nil, err
				}
				defer func() {
					go active.Release(context.TODO())
				}()
				root = active
			}
		} else {
			mounts = append(mounts, executor.Mount{Src: mountable, Dest: m.Dest, Readonly: m.Readonly, Selector: m.Selector})
		}
	}

	sort.Slice(mounts, func(i, j int) bool {
		return mounts[i].Dest < mounts[j].Dest
	})

	meta := executor.Meta{
		Args:           e.op.Meta.Args,
		Env:            e.op.Meta.Env,
		Cwd:            e.op.Meta.Cwd,
		User:           e.op.Meta.User,
		ReadonlyRootFS: readonlyRootFS,
	}

	stdout, stderr := logs.NewLogStreams(ctx)
	defer stdout.Close()
	defer stderr.Close()

	if err := e.exec.Exec(ctx, meta, root, mounts, nil, stdout, stderr); err != nil {
		return nil, errors.Wrapf(err, "executor failed running %v", meta.Args)
	}

	refs := []solver.Result{}
	for i, out := range outputs {
		if mutable, ok := out.(cache.MutableRef); ok {
			ref, err := mutable.Commit(ctx)
			if err != nil {
				return nil, errors.Wrapf(err, "error committing %s", mutable.ID())
			}
			refs = append(refs, worker.NewWorkerRefResult(ref, e.w))
		} else {
			refs = append(refs, worker.NewWorkerRefResult(out.(cache.ImmutableRef), e.w))
		}
		outputs[i] = nil
	}
	return refs, nil
}
