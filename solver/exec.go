package solver

import (
	"encoding/json"
	"fmt"
	"path"
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

const execCacheType = "buildkit.exec.v0"

type execOp struct {
	op        *pb.ExecOp
	cm        cache.Manager
	w         worker.Worker
	numInputs int
}

func newExecOp(v Vertex, op *pb.Op_Exec, cm cache.Manager, w worker.Worker) (Op, error) {
	return &execOp{
		op:        op.Exec,
		cm:        cm,
		w:         w,
		numInputs: len(v.Inputs()),
	}, nil
}

func (e *execOp) CacheKey(ctx context.Context) (digest.Digest, error) {
	dt, err := json.Marshal(struct {
		Type string
		Exec *pb.ExecOp
	}{
		Type: execCacheType,
		Exec: e.op,
	})
	if err != nil {
		return "", err
	}

	return digest.FromBytes(dt), nil
}

func (e *execOp) Run(ctx context.Context, inputs []Reference) ([]Reference, error) {
	var mounts []worker.Mount
	var outputs []Reference
	var root cache.Mountable

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
			ref, ok = toImmutableRef(inp)
			if !ok {
				return nil, errors.Errorf("invalid reference for exec %T", inputs[int(m.Input)])
			}
			mountable = ref
		}
		if m.Output != pb.SkipOutput {
			if m.Readonly && ref != nil && m.Dest != pb.RootMount { // exclude read-only rootfs
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
			mounts = append(mounts, worker.Mount{Src: mountable, Dest: m.Dest, Readonly: m.Readonly, Selector: m.Selector})
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

	if err := e.w.Exec(ctx, meta, root, mounts, nil, stdout, stderr); err != nil {
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
	}{
		Type: execCacheType,
		Exec: &ecopy,
	})
	if err != nil {
		return "", nil, err
	}

	return digest.FromBytes(dt), contentInputs, nil
}
