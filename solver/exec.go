package solver

import (
	"encoding/json"
	"io"
	"os"
	"sort"

	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/progress"
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

func newExecOp(op *pb.Op_Exec, cm cache.Manager, w worker.Worker) (Op, error) {
	return &execOp{
		op: op.Exec,
		cm: cm,
		w:  w,
	}, nil
}

func (e *execOp) CacheKey(ctx context.Context, inputs []string) (string, error) {
	dt, err := json.Marshal(struct {
		Inputs []string
		Exec   *pb.ExecOp
	}{
		Inputs: inputs,
		Exec:   e.op,
	})
	if err != nil {
		return "", err
	}
	return digest.FromBytes(dt).String(), nil
}

func (e *execOp) Run(ctx context.Context, inputs []Reference) ([]Reference, error) {
	var mounts []worker.Mount
	var outputs []cache.MutableRef
	var root cache.Mountable

	defer func() {
		for _, o := range outputs {
			if o != nil {
				s, err := o.Freeze() // TODO: log error
				if err == nil {
					go s.Release(ctx)
				}
			}
		}
	}()

	for _, m := range e.op.Mounts {
		var mountable cache.Mountable
		if int(m.Input) > len(inputs) {
			return nil, errors.Errorf("missing input %d", m.Input)
		}
		inp := inputs[int(m.Input)]
		if sys, ok := inp.(interface {
			Sys() Reference
		}); ok {
			inp = sys.Sys()
		}
		ref, ok := inp.(cache.ImmutableRef)
		if !ok {
			return nil, errors.Errorf("invalid reference for exec %T", inputs[int(m.Input)])
		}
		mountable = ref
		if m.Output != -1 {
			active, err := e.cm.New(ctx, ref) // TODO: should be method
			if err != nil {
				return nil, err
			}
			outputs = append(outputs, active)
			mountable = active
		}
		if m.Dest == pb.RootMount {
			root = mountable
		} else {
			mounts = append(mounts, worker.Mount{Src: mountable, Dest: m.Dest})
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

	stdout := newStreamWriter(ctx, 1)
	defer stdout.Close()
	stderr := newStreamWriter(ctx, 2)
	defer stderr.Close()

	if err := e.w.Exec(ctx, meta, root, mounts, stdout, stderr); err != nil {
		return nil, errors.Wrapf(err, "worker failed running %v", meta.Args)
	}

	refs := []Reference{}
	for i, o := range outputs {
		ref, err := o.ReleaseAndCommit(ctx)
		if err != nil {
			return nil, errors.Wrapf(err, "error committing %s", o.ID())
		}
		refs = append(refs, ref)
		outputs[i] = nil
	}
	return refs, nil
}

func newStreamWriter(ctx context.Context, stream int) io.WriteCloser {
	pw, _, _ := progress.FromContext(ctx)
	return &streamWriter{
		pw:     pw,
		stream: stream,
	}
}

type streamWriter struct {
	pw     progress.Writer
	stream int
}

func (sw *streamWriter) Write(dt []byte) (int, error) {
	sw.pw.Write(identity.NewID(), client.VertexLog{
		Stream: sw.stream,
		Data:   append([]byte{}, dt...),
	})
	// TODO: remove debug
	switch sw.stream {
	case 1:
		return os.Stdout.Write(dt)
	case 2:
		return os.Stderr.Write(dt)
	default:
		return 0, errors.Errorf("invalid stream %d", sw.stream)
	}
}

func (sw *streamWriter) Close() error {
	return sw.pw.Close()
}
