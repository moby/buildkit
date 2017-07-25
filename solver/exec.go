package solver

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/progress/logs"
	"github.com/moby/buildkit/worker"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
	"golang.org/x/net/context"
)

type execOp struct {
	op *pb.ExecOp
	cm cache.Manager
	w  worker.Worker
}

func newExecOp(_ Vertex, op *pb.Op_Exec, cm cache.Manager, w worker.Worker) (Op, error) {
	return &execOp{
		op: op.Exec,
		cm: cm,
		w:  w,
	}, nil
}

func (e *execOp) CacheKey(ctx context.Context, inputs []string) (string, int, error) {
	dt, err := json.Marshal(struct {
		Inputs []string
		Exec   *pb.ExecOp
	}{
		Inputs: inputs,
		Exec:   e.op,
	})
	if err != nil {
		return "", 0, err
	}
	numRefs := 0
	for _, m := range e.op.Mounts {
		if m.Output != pb.SkipOutput {
			numRefs++
		}
	}

	return digest.FromBytes(dt).String(), numRefs, nil
}

func (e *execOp) Run(ctx context.Context, inputs []Reference) ([]Reference, error) {
	var mounts []worker.Mount
	var outputs []Reference
	var root cache.Mountable

	defer func() {
		for _, o := range outputs {
			if o != nil {
				go o.Release(ctx)
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
			ref, ok = toImmutableRef(inp)
			if !ok {
				return nil, errors.Errorf("invalid reference for exec %T", inputs[int(m.Input)])
			}
			mountable = ref
		}
		if m.Output != pb.SkipOutput {
			if m.Readonly && ref != nil {
				outputs = append(outputs, newSharedRef(ref).Clone())
			} else {
				active, err := e.cm.New(ctx, ref, cache.WithDescription(fmt.Sprintf("mount %s from exec %s", m.Dest, strings.Join(e.op.Meta.Args, " ")))) // TODO: should be method
				if err != nil {
					return nil, err
				}
				outputs = append(outputs, active)
				mountable = active
			}
		}
		if m.Dest == pb.RootMount {
			root = mountable
		} else {
			mounts = append(mounts, worker.Mount{Src: mountable, Dest: m.Dest, Readonly: m.Readonly})
		}
	}

	sort.Slice(mounts, func(i, j int) bool {
		return mounts[i].Dest < mounts[j].Dest
	})

	meta := worker.Meta{
		Args: e.op.Meta.Args,
		Env:  e.op.Meta.Env,
		Cwd:  e.op.Meta.Cwd,
	}

	stdout, stderr := logs.NewLogStreams(ctx)
	defer stdout.Close()
	defer stderr.Close()

	if err := e.w.Exec(ctx, meta, root, mounts, stdout, stderr); err != nil {
		return nil, errors.Wrapf(err, "worker failed running %v", meta.Args)
	}

	refs := []Reference{}
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
