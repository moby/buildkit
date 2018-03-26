package llbop

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"runtime"
	"sort"
	"strings"

	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/executor"
	"github.com/moby/buildkit/solver"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/progress/logs"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
)

const execCacheType = "buildkit.exec.v0"

type execOp struct {
	op        *pb.ExecOp
	cm        cache.Manager
	exec      executor.Executor
	numInputs int
}

func NewExecOp(v solver.Vertex, op *pb.Op_Exec, cm cache.Manager, exec executor.Executor) (solver.Op, error) {
	return &execOp{
		op:        op.Exec,
		cm:        cm,
		exec:      exec,
		numInputs: len(v.Inputs()),
	}, nil
}

func (e *execOp) CacheKey(ctx context.Context) (digest.Digest, error) {
	dt, err := json.Marshal(struct {
		Type string
		Exec *pb.ExecOp
		OS   string
		Arch string
	}{
		Type: execCacheType,
		Exec: e.op,
		OS:   runtime.GOOS,
		Arch: runtime.GOARCH,
	})
	if err != nil {
		return "", err
	}

	return digest.FromBytes(dt), nil
}

func (e *execOp) Run(ctx context.Context, inputs []solver.Ref) ([]solver.Ref, error) {
	var mounts []executor.Mount
	var outputs []solver.Ref
	var root cache.Mountable
	var readonlyRootFS bool

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
			var ok bool
			ref, ok = solver.ToImmutableRef(inp)
			if !ok {
				return nil, errors.Errorf("invalid reference for exec %T", inputs[int(m.Input)])
			}
			mountable = ref
		}
		if m.Output != pb.SkipOutput {
			if m.Readonly && ref != nil && m.Dest != pb.RootMount { // exclude read-only rootfs
				outputs = append(outputs, solver.NewSharedRef(ref).Clone())
			} else {
				active, err := e.cm.New(ctx, ref, cache.WithDescription(fmt.Sprintf("mount %s from exec %s", m.Dest, strings.Join(e.op.Meta.Args, " ")))) // TODO: should be method
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
				// XXX this duplicates a case from above.
				active, err := e.cm.New(ctx, ref, cache.WithDescription(fmt.Sprintf("mount %s from exec %s", m.Dest, strings.Join(e.op.Meta.Args, " ")))) // TODO: should be method
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

	refs := []solver.Ref{}
	for i, o := range outputs {
		if mutable, ok := o.(cache.MutableRef); ok {
			ref, err := mutable.Commit(ctx)
			if err != nil {
				return nil, errors.Wrapf(err, "error committing %s", mutable.ID())
			}
			refs = append(refs, ref)
		} else {
			refs = append(refs, o)
		}
		outputs[i] = nil
	}
	return refs, nil
}

func (e *execOp) ContentMask(ctx context.Context) (digest.Digest, [][]string, error) {
	// contentKey for exec uses content based checksum for mounts and definition
	// based checksum for root

	skipped := make(map[int]struct{}, 0)

	type src struct {
		index    int
		selector string
	}

	srcsMap := make(map[src]struct{})
	mountsCopy := make([]*pb.Mount, len(e.op.Mounts))
	for i, m := range e.op.Mounts {
		copy := *m
		mountsCopy[i] = &copy
		if m.Input != pb.Empty {
			if m.Dest != pb.RootMount && m.Readonly { // could also include rw if they don't have a selector, but not sure if helps performance
				srcsMap[src{int(m.Input), path.Join("/", m.Selector)}] = struct{}{}
				mountsCopy[i].Selector = ""
			} else {
				skipped[int(m.Input)] = struct{}{}
			}
		}
	}
	if len(srcsMap) == 0 {
		return "", nil, nil
	}

	contentInputs := make([][]string, e.numInputs)
	for in := range srcsMap {
		contentInputs[in.index] = append(contentInputs[in.index], in.selector)
	}
	// TODO: remove nested directories

	for k := range contentInputs {
		sort.Strings(contentInputs[k])
	}

	ecopy := *e.op
	ecopy.Mounts = mountsCopy

	dt, err := json.Marshal(struct {
		Type string
		Exec *pb.ExecOp
		OS   string
		Arch string
	}{
		Type: execCacheType,
		Exec: &ecopy,
		OS:   runtime.GOOS,
		Arch: runtime.GOARCH,
	})
	if err != nil {
		return "", nil, err
	}

	return digest.FromBytes(dt), contentInputs, nil
}
