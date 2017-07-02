package solver

import (
	"context"
	"io"
	"os"

	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/progress"
	"github.com/moby/buildkit/worker"
	"github.com/pkg/errors"
)

func runExecOp(ctx context.Context, cm cache.Manager, w worker.Worker, op *pb.Op_Exec, inputs []cache.ImmutableRef) ([]cache.ImmutableRef, error) {
	mounts := make(map[string]cache.Mountable)

	var outputs []cache.MutableRef

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

	for _, m := range op.Exec.Mounts {
		var mountable cache.Mountable
		if int(m.Input) > len(inputs) {
			return nil, errors.Errorf("missing input %d", m.Input)
		}
		ref := inputs[int(m.Input)]
		mountable = ref
		if m.Output != -1 {
			active, err := cm.New(ctx, ref) // TODO: should be method
			if err != nil {
				return nil, err
			}
			outputs = append(outputs, active)
			mountable = active
		}
		mounts[m.Dest] = mountable
	}

	meta := worker.Meta{
		Args: op.Exec.Meta.Args,
		Env:  op.Exec.Meta.Env,
		Cwd:  op.Exec.Meta.Cwd,
	}

	stdout := newStreamWriter(ctx, 1)
	defer stdout.Close()
	stderr := newStreamWriter(ctx, 2)
	defer stderr.Close()

	if err := w.Exec(ctx, meta, mounts, stdout, stderr); err != nil {
		return nil, errors.Wrapf(err, "worker failed running %v", meta.Args)
	}

	refs := []cache.ImmutableRef{}
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
