package solver

import (
	"fmt"

	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/exporter"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/source"
	"github.com/moby/buildkit/util/progress"
	"github.com/moby/buildkit/worker"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"golang.org/x/net/context"
	"golang.org/x/sync/errgroup"
)

type LLBOpt struct {
	SourceManager    *source.Manager
	CacheManager     cache.Manager // TODO: this shouldn't be needed before instruction cache
	Worker           worker.Worker
	InstructionCache InstructionCache
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
	}, opt.InstructionCache)
}

// ResolveOpFunc finds an Op implementation for a vertex
type ResolveOpFunc func(Vertex) (Op, error)

// Reference is a reference to the object passed through the build steps.
type Reference interface {
	Release(context.Context) error
}

// Op is an implementation for running a vertex
type Op interface {
	CacheKey(context.Context, []string) (string, int, error)
	Run(ctx context.Context, inputs []Reference) (outputs []Reference, err error)
}

type InstructionCache interface {
	Lookup(ctx context.Context, key string) (interface{}, error) // TODO: regular ref
	Set(key string, ref interface{}) error
}

type Solver struct {
	resolve     ResolveOpFunc
	jobs        *jobList
	activeState activeState
	cache       InstructionCache
}

func New(resolve ResolveOpFunc, cache InstructionCache) *Solver {
	return &Solver{resolve: resolve, jobs: newJobList(), cache: cache}
}

func (s *Solver) Solve(ctx context.Context, id string, v Vertex, exp exporter.ExporterInstance) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	pr, ctx, closeProgressWriter := progress.NewContext(ctx)

	origVertex := v

	defer closeProgressWriter()

	if len(v.Inputs()) > 0 { // TODO: detect op_return better
		v = v.Inputs()[0].Vertex
	}

	vv := toInternalVertex(v)
	solveVertex := vv

	if exp != nil {
		vv = &vertex{digest: origVertex.Digest(), name: exp.Name()}
		vv.inputs = []*input{{index: 0, vertex: solveVertex}}
		vv.initClientVertex()
	}

	j, err := s.jobs.new(ctx, id, vv, pr)
	if err != nil {
		return err
	}

	refs, err := s.getRefs(ctx, j, solveVertex)
	s.activeState.cancel(j)
	if err != nil {
		return err
	}

	defer func() {
		for _, r := range refs {
			r.Release(context.TODO())
		}
	}()

	for _, ref := range refs {
		immutable, ok := toImmutableRef(ref)
		if !ok {
			return errors.Errorf("invalid reference for exporting: %T", ref)
		}
		if err := immutable.Finalize(ctx); err != nil {
			return err
		}
	}

	if exp != nil {
		immutable, ok := toImmutableRef(refs[0])
		if !ok {
			return errors.Errorf("invalid reference for exporting: %T", refs[0])
		}
		vv.notifyStarted(ctx)
		pw, _, ctx := progress.FromContext(ctx, progress.WithMetadata("vertex", vv.Digest()))
		defer pw.Close()
		err := exp.Export(ctx, immutable)
		vv.notifyCompleted(ctx, false, err)
		if err != nil {
			return err
		}
	}
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

func (s *Solver) getCacheKey(ctx context.Context, j *job, g *vertex) (cacheKey string, numRefs int, retErr error) {
	state, err := s.activeState.vertexState(j, g.digest, func() (Op, error) {
		return s.resolve(g)
	})
	if err != nil {
		return "", 0, err
	}

	inputs := make([]string, len(g.inputs))
	if len(g.inputs) > 0 {
		eg, ctx := errgroup.WithContext(ctx)
		for i, in := range g.inputs {
			func(i int, in *vertex, index int) {
				eg.Go(func() error {
					k, _, err := s.getCacheKey(ctx, j, in)
					if err != nil {
						return err
					}
					inputs[i] = fmt.Sprintf("%s.%d", k, index)
					return nil
				})
			}(i, in.vertex, in.index)
		}
		if err := eg.Wait(); err != nil {
			return "", 0, err
		}
	}

	pw, _, ctx := progress.FromContext(ctx, progress.WithMetadata("vertex", g.Digest()))
	defer pw.Close()

	if len(g.inputs) == 0 {
		g.notifyStarted(ctx)
		defer func() {
			g.notifyCompleted(ctx, false, retErr)
		}()
	}

	return state.GetCacheKey(ctx, func(ctx context.Context, op Op) (string, int, error) {
		return op.CacheKey(ctx, inputs)
	})
}

func (s *Solver) getRefs(ctx context.Context, j *job, g *vertex) (retRef []Reference, retErr error) {
	state, err := s.activeState.vertexState(j, g.digest, func() (Op, error) {
		return s.resolve(g)
	})
	if err != nil {
		return nil, err
	}

	var cacheKey string
	if s.cache != nil {
		var err error
		var numRefs int
		cacheKey, numRefs, err = s.getCacheKey(ctx, j, g)
		if err != nil {
			return nil, err
		}
		cacheRefs := make([]Reference, 0, numRefs)
		// check if all current refs are already cached
		for i := 0; i < numRefs; i++ {
			ref, err := s.cache.Lookup(ctx, fmt.Sprintf("%s.%d", cacheKey, i))
			if err != nil {
				return nil, err
			}
			if ref == nil { // didn't find ref, release all
				for _, ref := range cacheRefs {
					ref.Release(context.TODO())
				}
				break
			}
			cacheRefs = append(cacheRefs, ref.(Reference))
			if len(cacheRefs) == numRefs { // last item
				g.recursiveMarkCached(ctx)
				return cacheRefs, nil
			}
		}
	}

	// refs contains all outputs for all input vertexes
	refs := make([][]*sharedRef, len(g.inputs))
	if len(g.inputs) > 0 {
		eg, ctx := errgroup.WithContext(ctx)
		for i, in := range g.inputs {
			func(i int, in *vertex, index int) {
				eg.Go(func() error {
					if s.cache != nil {
						k, numRefs, err := s.getCacheKey(ctx, j, in)
						if err != nil {
							return err
						}
						ref, err := s.cache.Lookup(ctx, fmt.Sprintf("%s.%d", k, index))
						if err != nil {
							return err
						}
						if ref != nil {
							if ref, ok := toImmutableRef(ref.(Reference)); ok {
								refs[i] = make([]*sharedRef, numRefs)
								refs[i][index] = newSharedRef(ref)
								in.recursiveMarkCached(ctx)
								return nil
							}
						}
					}

					// execute input vertex
					r, err := s.getRefs(ctx, j, in)
					if err != nil {
						return err
					}
					for _, r := range r {
						refs[i] = append(refs[i], newSharedRef(r))
					}
					if ref, ok := toImmutableRef(r[index].(Reference)); ok {
						// make sure input that is required by next step does not get released in case build is cancelled
						if err := cache.CachePolicyRetain(ref); err != nil {
							return err
						}
					}
					return nil
				})
			}(i, in.vertex, in.index)
		}
		err := eg.Wait()
		if err != nil {
			for _, r := range refs {
				for _, r := range r {
					if r != nil {
						go r.Release(context.TODO())
					}
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
			if r != nil {
				go r.Release(context.TODO())
			}
		}
	}

	pw, _, ctx := progress.FromContext(ctx, progress.WithMetadata("vertex", g.Digest()))
	defer pw.Close()

	g.notifyStarted(ctx)
	defer func() {
		g.notifyCompleted(ctx, false, retErr)
	}()

	return state.GetRefs(ctx, func(ctx context.Context, op Op) ([]Reference, error) {
		refs, err := op.Run(ctx, inputRefs)
		if err != nil {
			return nil, err
		}
		if s.cache != nil {
			for i, ref := range refs {
				if err := s.cache.Set(fmt.Sprintf("%s.%d", cacheKey, i), originRef(ref)); err != nil {
					logrus.Errorf("failed to save cache for %s: %v", cacheKey, err)
				}
			}
		}
		return refs, nil
	})
}
