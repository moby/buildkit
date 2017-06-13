package solver

import (
	"context"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
	"github.com/tonistiigi/buildkit_poc/cache"
	"github.com/tonistiigi/buildkit_poc/client"
	"github.com/tonistiigi/buildkit_poc/solver/pb"
	"github.com/tonistiigi/buildkit_poc/source"
	"github.com/tonistiigi/buildkit_poc/util/progress"
	"github.com/tonistiigi/buildkit_poc/worker"
)

type opVertex struct {
	mu     sync.Mutex
	op     *pb.Op
	inputs []*opVertex
	refs   []cache.ImmutableRef
	err    error
	dgst   digest.Digest
	vtx    client.Vertex
}

func Load(ops [][]byte) (*opVertex, error) {
	if len(ops) == 0 {
		return nil, errors.New("invalid empty definition")
	}

	m := make(map[digest.Digest]*pb.Op)

	var lastOp *pb.Op
	var dgst digest.Digest

	for i, dt := range ops {
		var op pb.Op
		if err := (&op).Unmarshal(dt); err != nil {
			return nil, errors.Wrap(err, "failed to parse op")
		}
		lastOp = &op
		dgst = digest.FromBytes(dt)
		if i != len(ops)-1 {
			m[dgst] = &op
		}
		// logrus.Debugf("op %d %s %#v", i, dgst, op)
	}

	cache := make(map[digest.Digest]*opVertex)

	// TODO: validate the connections
	vtx, err := loadReqursive(dgst, lastOp, m, cache)
	if err != nil {
		return nil, err
	}

	return vtx, err
}

func loadReqursive(dgst digest.Digest, op *pb.Op, inputs map[digest.Digest]*pb.Op, cache map[digest.Digest]*opVertex) (*opVertex, error) {
	if v, ok := cache[dgst]; ok {
		return v, nil
	}
	vtx := &opVertex{op: op, dgst: dgst}
	inputDigests := make([]digest.Digest, 0, len(op.Inputs))
	for _, in := range op.Inputs {
		dgst := digest.Digest(in.Digest)
		inputDigests = append(inputDigests, dgst)
		op, ok := inputs[dgst]
		if !ok {
			return nil, errors.Errorf("failed to find %s", in)
		}
		sub, err := loadReqursive(dgst, op, inputs, cache)
		if err != nil {
			return nil, err
		}
		vtx.inputs = append(vtx.inputs, sub)
	}
	vtx.vtx = client.Vertex{
		Inputs: inputDigests,
		Name:   vtx.name(),
		ID:     dgst,
	}
	cache[dgst] = vtx
	return vtx, nil
}

type Opt struct {
	SourceManager *source.Manager
	CacheManager  cache.Manager // TODO: this shouldn't be needed before instruction cache
	Worker        worker.Worker
}

func (g *opVertex) inputRequiresExport(i int) bool {
	return true // TODO
}

type Solver struct {
	opt  Opt
	jobs jobs
}

func New(opt Opt) *Solver {
	return &Solver{opt: opt}
}

func (s *Solver) Solve(ctx context.Context, id string, g *opVertex) error {
	// ctx, cancel := context.WithCancel(ctx)
	// defer cancel()

	pr, ctx, closeProgressWriter := progress.NewContext(ctx)

	_, err := s.jobs.new(id, g, pr)
	if err != nil {
		return err
	}

	err = g.solve(ctx, s.opt) // TODO: separate exporting
	closeProgressWriter()
	if err != nil {
		return err
	}

	g.release(ctx)
	// TODO: export final vertex state
	return err
}

func (s *Solver) Status(ctx context.Context, id string, statusChan chan *client.SolveStatus) error {
	j, err := s.jobs.get(id)
	if err != nil {
		return nil
	}
	return j.pipe(ctx, statusChan)
}

func (g *opVertex) release(ctx context.Context) (retErr error) {
	for _, i := range g.inputs {
		if err := i.release(ctx); err != nil {
			retErr = err
		}
	}
	for _, ref := range g.refs {
		if ref != nil {
			if err := ref.Release(ctx); err != nil {
				retErr = err
			}
		}
	}
	return retErr
}

func (g *opVertex) getInputRef(i int) cache.ImmutableRef {
	input := g.op.Inputs[i]
	for _, v := range g.inputs {
		if v.dgst == digest.Digest(input.Digest) {
			return v.refs[input.Index]
		}
	}
	return nil
}

func (g *opVertex) solve(ctx context.Context, opt Opt) (retErr error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.err != nil {
		return g.err
	}
	if len(g.refs) > 0 {
		return nil
	}

	defer func() {
		if retErr != nil {
			g.err = retErr
		}
	}()

	if len(g.inputs) > 0 {
		eg, ctx := errgroup.WithContext(ctx)

		for _, in := range g.inputs {
			eg.Go(func() error {
				if err := in.solve(ctx, opt); err != nil {
					return err
				}
				return nil
			})
		}
		err := eg.Wait()
		if err != nil {
			return err
		}
	}

	pw, _, ctx := progress.FromContext(ctx, g.dgst.String())
	defer pw.Done()

	g.notifyStarted(pw)
	defer g.notifyComplete(pw)

	switch op := g.op.Op.(type) {
	case *pb.Op_Source:
		id, err := source.FromString(op.Source.Identifier)
		if err != nil {
			return err
		}
		ref, err := opt.SourceManager.Pull(ctx, id)
		if err != nil {
			return err
		}
		g.refs = []cache.ImmutableRef{ref}
	case *pb.Op_Exec:

		mounts := make(map[string]cache.Mountable)

		var outputs []cache.MutableRef

		defer func() {
			for _, o := range outputs {
				if o != nil {
					s, err := o.Freeze() // TODO: log error
					if err == nil {
						s.Release(ctx)
					}
				}
			}
		}()

		for _, m := range op.Exec.Mounts {
			var mountable cache.Mountable
			ref := g.getInputRef(int(m.Input))
			mountable = ref
			if m.Output != -1 {
				active, err := opt.CacheManager.New(ctx, ref) // TODO: should be method
				if err != nil {
					return err
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

		if err := opt.Worker.Exec(ctx, meta, mounts, os.Stderr, os.Stderr); err != nil {
			return errors.Wrapf(err, "worker failed running %v", meta.Args)
		}

		g.refs = []cache.ImmutableRef{}

		for i, o := range outputs {
			ref, err := o.ReleaseAndCommit(ctx)
			if err != nil {
				return errors.Wrapf(err, "error committing %s", ref.ID())
			}
			g.refs = append(g.refs, ref)
			outputs[i] = nil
		}

	default:
		return errors.Errorf("invalid op type")
	}
	return nil
}

func (g *opVertex) notifyStarted(pw progress.ProgressWriter) {
	g.vtx.Started = time.Now()
	pw.Write(g.vtx)
}

func (g *opVertex) notifyComplete(pw progress.ProgressWriter) {
	g.vtx.Completed = time.Now()
	pw.Write(g.vtx)
}

func (g *opVertex) name() string {
	switch op := g.op.Op.(type) {
	case *pb.Op_Source:
		return op.Source.Identifier
	case *pb.Op_Exec:
		name := strings.Join(op.Exec.Meta.Args, " ")
		if len(name) > 22 { // TODO: const
			name = name[:20] + "..."
		}
		return name
	default:
		return "unknown"
	}
}
