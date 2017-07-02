package solver

import (
	"strings"
	"sync"
	"time"

	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/source"
	"github.com/moby/buildkit/util/progress"
	"github.com/moby/buildkit/worker"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
	"golang.org/x/net/context"
	"golang.org/x/sync/errgroup"
)

type Opt struct {
	SourceManager *source.Manager
	CacheManager  cache.Manager // TODO: this shouldn't be needed before instruction cache
	Worker        worker.Worker
}

type Solver struct {
	opt    Opt
	jobs   *jobList
	active refCache
}

func New(opt Opt) *Solver {
	return &Solver{opt: opt, jobs: newJobList()}
}

func (s *Solver) Solve(ctx context.Context, id string, g *opVertex) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	pr, ctx, closeProgressWriter := progress.NewContext(ctx)

	if len(g.inputs) > 0 { // TODO: detect op_return better
		g = g.inputs[0]
	}

	j, err := s.jobs.new(ctx, id, g, pr)
	if err != nil {
		return err
	}

	refs, err := s.getRefs(ctx, j, j.g)
	closeProgressWriter()
	s.active.cancel(j)
	if err != nil {
		return err
	}

	for _, r := range refs {
		r.Release(context.TODO())
	}
	// TODO: export final vertex state
	return err
}

func (s *Solver) Status(ctx context.Context, id string, statusChan chan *client.SolveStatus) error {
	j, err := s.jobs.get(id)
	if err != nil {
		return err
	}
	defer close(statusChan)
	return j.pipe(ctx, statusChan)
}

func (s *Solver) getRefs(ctx context.Context, j *job, g *opVertex) (retRef []cache.ImmutableRef, retErr error) {

	s.active.probe(j, g.dgst) // this registers the key with the job

	// refs contains all outputs for all input vertexes
	refs := make([][]*sharedRef, len(g.inputs))
	if len(g.inputs) > 0 {
		eg, ctx := errgroup.WithContext(ctx)
		for i, in := range g.inputs {
			func(i int, in *opVertex) {
				eg.Go(func() error {
					r, err := s.getRefs(ctx, j, in)
					if err != nil {
						return err
					}
					for _, r := range r {
						refs[i] = append(refs[i], newSharedRef(r))
					}
					return nil
				})
			}(i, in)
		}
		err := eg.Wait()
		if err != nil {
			for _, r := range refs {
				for _, r := range r {
					go r.Release(context.TODO())
				}
			}
			return nil, err
		}
	}

	// determine the inputs that were needed
	inputs := make([]cache.ImmutableRef, 0, len(g.op.Inputs))
	for _, inp := range g.op.Inputs {
		for i, v := range g.inputs {
			if v.dgst == digest.Digest(inp.Digest) {
				inputs = append(inputs, refs[i][int(inp.Index)].Clone())
			}
		}
	}

	// release anything else
	for _, r := range refs {
		for _, r := range r {
			go r.Release(context.TODO())
		}
	}

	pw, _, ctx := progress.FromContext(ctx, progress.WithMetadata("vertex", g.dgst))
	defer pw.Close()

	g.notifyStarted(ctx)
	defer func() {
		g.notifyCompleted(ctx, retErr)
	}()

	_, err := s.active.Do(ctx, g.dgst.String(), func(doctx context.Context) (interface{}, error) {
		if hit := s.active.probe(j, g.dgst); hit {
			if err := s.active.writeProgressSnapshot(ctx, g.dgst); err != nil {
				return nil, err
			}
			return nil, nil
		}
		refs, err := s.runVertex(doctx, g, inputs)
		if err != nil {
			return nil, err
		}
		s.active.set(doctx, g.dgst, refs)
		return nil, nil
	})
	if err != nil {
		return nil, err
	}
	return s.active.get(g.dgst)
}

func (s *Solver) runVertex(ctx context.Context, g *opVertex, inputs []cache.ImmutableRef) ([]cache.ImmutableRef, error) {
	switch op := g.op.Op.(type) {
	case *pb.Op_Source:
		return runSourceOp(ctx, s.opt.SourceManager, op)
	case *pb.Op_Exec:
		return runExecOp(ctx, s.opt.CacheManager, s.opt.Worker, op, inputs)
	default:
		return nil, errors.Errorf("invalid op type %T", g.op.Op)
	}
}

type opVertex struct {
	mu     sync.Mutex
	op     *pb.Op
	inputs []*opVertex
	err    error
	dgst   digest.Digest
	vtx    client.Vertex
}

func (g *opVertex) inputRequiresExport(i int) bool {
	return true // TODO
}

func (g *opVertex) notifyStarted(ctx context.Context) {
	pw, _, _ := progress.FromContext(ctx)
	defer pw.Close()
	now := time.Now()
	g.vtx.Started = &now
	pw.Write(g.dgst.String(), g.vtx)
}

func (g *opVertex) notifyCompleted(ctx context.Context, err error) {
	pw, _, _ := progress.FromContext(ctx)
	defer pw.Close()
	now := time.Now()
	g.vtx.Completed = &now
	if err != nil {
		g.vtx.Error = err.Error()
	}
	pw.Write(g.dgst.String(), g.vtx)
}

func (g *opVertex) name() string {
	switch op := g.op.Op.(type) {
	case *pb.Op_Source:
		return op.Source.Identifier
	case *pb.Op_Exec:
		return strings.Join(op.Exec.Meta.Args, " ")
	default:
		return "unknown"
	}
}
