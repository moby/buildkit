package solver

import (
	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/source"
	"github.com/moby/buildkit/util/progress"
	"github.com/moby/buildkit/worker"
	"github.com/pkg/errors"
	"golang.org/x/net/context"
	"golang.org/x/sync/errgroup"
)

type LLBOpt struct {
	SourceManager *source.Manager
	CacheManager  cache.Manager // TODO: this shouldn't be needed before instruction cache
	Worker        worker.Worker
}

func NewLLBSolver(opt LLBOpt) *Solver {
	return New(func(v Vertex) (Op, error) {
		switch op := v.Sys().(type) {
		case *pb.Op_Source:
			return newSourceOp(op, opt.SourceManager)
		case *pb.Op_Exec:
			return newExecOp(op, opt.CacheManager, opt.Worker)
		default:
			return nil, errors.Errorf("invalid op type %T", op)
		}
	})
}

// ResolveOpFunc finds an Op implementation for a vertex
type ResolveOpFunc func(Vertex) (Op, error)

// Reference is a reference to the object passed through the build steps.
type Reference interface {
	Release(context.Context) error
}

// Op is an implementation for running a vertex
type Op interface {
	// CacheKeys(context.Context, [][]string) ([]string, error)
	Run(ctx context.Context, inputs []Reference) (outputs []Reference, err error)
}

// type Cache interface {
// 	Lookup(context.Context, string) ([]Reference, error)
// }

type Solver struct {
	resolve ResolveOpFunc
	jobs    *jobList
	active  refCache
}

func New(resolve ResolveOpFunc) *Solver {
	return &Solver{resolve: resolve, jobs: newJobList()}
}

func (s *Solver) Solve(ctx context.Context, id string, v Vertex) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	pr, ctx, closeProgressWriter := progress.NewContext(ctx)

	if len(v.Inputs()) > 0 { // TODO: detect op_return better
		v = v.Inputs()[0].Vertex
	}

	vv := toInternalVertex(v)

	j, err := s.jobs.new(ctx, id, vv, pr)
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

func (s *Solver) getRefs(ctx context.Context, j *job, g *vertex) (retRef []Reference, retErr error) {

	s.active.probe(j, g.digest) // this registers the key with the job

	// refs contains all outputs for all input vertexes
	refs := make([][]*sharedRef, len(g.inputs))
	if len(g.inputs) > 0 {
		eg, ctx := errgroup.WithContext(ctx)
		for i, in := range g.inputs {
			func(i int, in *vertex) {
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
			}(i, in.vertex)
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
	inputRefs := make([]Reference, 0, len(g.inputs))
	for i, inp := range g.inputs {
		inputRefs = append(inputRefs, refs[i][inp.index].Clone())
	}

	defer func() {
		for _, r := range inputRefs {
			go r.Release(context.TODO())
		}
	}()

	// release anything else
	for _, r := range refs {
		for _, r := range r {
			go r.Release(context.TODO())
		}
	}

	pw, _, ctx := progress.FromContext(ctx, progress.WithMetadata("vertex", g.Digest()))
	defer pw.Close()

	g.notifyStarted(ctx)
	defer func() {
		g.notifyCompleted(ctx, retErr)
	}()

	_, err := s.active.Do(ctx, g.digest.String(), func(doctx context.Context) (interface{}, error) {
		if hit := s.active.probe(j, g.digest); hit {
			if err := s.active.writeProgressSnapshot(ctx, g.digest); err != nil {
				return nil, err
			}
			return nil, nil
		}
		op, err := s.resolve(g)
		if err != nil {
			return nil, err
		}
		refs, err := op.Run(doctx, inputRefs)
		if err != nil {
			return nil, err
		}
		s.active.set(doctx, g.digest, refs)
		return nil, nil
	})
	if err != nil {
		return nil, err
	}
	return s.active.get(g.digest)
}
