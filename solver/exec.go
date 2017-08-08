package solver

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/cache/contenthash"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/progress/logs"
	"github.com/moby/buildkit/worker"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
	"golang.org/x/net/context"
	"golang.org/x/sync/errgroup"
)

const execCacheType = "buildkit.exec.v0"

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

func (e *execOp) ContentKeys(ctx context.Context, inputs [][]digest.Digest, refs []Reference) ([]digest.Digest, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	// contentKey for exec uses content based checksum for mounts and definition
	// based checksum for root

	rootIndex := -1
	skip := true
	srcs := make([]string, len(refs))
	for _, m := range e.op.Mounts {
		if m.Input != pb.Empty {
			if m.Dest != pb.RootMount {
				srcs[int(m.Input)] = "/" // TODO: selector
				skip = false
			} else {
				rootIndex = int(m.Input)
			}
		}
	}
	if skip {
		return nil, nil
	}

	dgsts := make([]digest.Digest, len(refs))
	eg, ctx := errgroup.WithContext(ctx)
	for i, ref := range refs {
		if srcs[i] == "" {
			continue
		}
		func(i int, ref Reference) {
			eg.Go(func() error {
				ref, ok := toImmutableRef(ref)
				if !ok {
					return errors.Errorf("invalid reference")
				}
				dgst, err := contenthash.Checksum(ctx, ref, srcs[i])
				if err != nil {
					return err
				}
				dgsts[i] = dgst
				return nil
			})
		}(i, ref)
	}
	if err := eg.Wait(); err != nil {
		return nil, err
	}

	var out []digest.Digest
	for _, cacheKeys := range inputs {
		dt, err := json.Marshal(struct {
			Type    string
			Sources []digest.Digest
			Root    digest.Digest
			Exec    *pb.ExecOp
		}{
			Type:    execCacheType,
			Sources: dgsts,
			Root:    cacheKeys[rootIndex],
			Exec:    e.op,
		})
		if err != nil {
			return nil, err
		}
		out = append(out, digest.FromBytes(dt))
	}

	return out, nil
}
