package solver

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/exporter"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/source"
	"github.com/moby/buildkit/util/progress"
	"github.com/moby/buildkit/worker"
	digest "github.com/opencontainers/go-digest"
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
	var s *Solver
	s = New(func(v Vertex) (Op, error) {
		switch op := v.Sys().(type) {
		case *pb.Op_Source:
			return newSourceOp(v, op, opt.SourceManager)
		case *pb.Op_Exec:
			return newExecOp(v, op, opt.CacheManager, opt.Worker)
		case *pb.Op_Build:
			return newBuildOp(v, op, s)
		default:
			return nil, errors.Errorf("invalid op type %T", op)
		}
	}, opt.InstructionCache)
	return s
}

// ResolveOpFunc finds an Op implementation for a vertex
type ResolveOpFunc func(Vertex) (Op, error)

// Reference is a reference to the object passed through the build steps.
type Reference interface {
	Release(context.Context) error
}

// Op is an implementation for running a vertex
type Op interface {
	CacheKey(context.Context) (digest.Digest, error)
	ContentKeys(context.Context, [][]digest.Digest, []Reference) ([]digest.Digest, error)
	Run(ctx context.Context, inputs []Reference) (outputs []Reference, err error)
}

type InstructionCache interface {
	Lookup(ctx context.Context, key digest.Digest) (interface{}, error) // TODO: regular ref
	Set(key digest.Digest, ref interface{}) error
	SetContentMapping(key digest.Digest, value interface{}) error
	GetContentMapping(dgst digest.Digest) ([]digest.Digest, error)
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

	if len(v.Inputs()) == 0 {
		return errors.New("required vertex needs to have inputs")
	}

	index := v.Inputs()[0].Index
	v = v.Inputs()[0].Vertex

	vv := toInternalVertex(v)
	solveVertex := vv

	if exp != nil {
		vv = &vertex{digest: origVertex.Digest(), name: exp.Name()}
		vv.inputs = []*input{{index: 0, vertex: solveVertex}}
		vv.initClientVertex()
	}

	ctx, j, err := s.jobs.new(ctx, id, vv, pr)
	if err != nil {
		return err
	}

	ref, err := s.getRef(ctx, solveVertex, index)
	s.activeState.cancel(j)
	if err != nil {
		return err
	}

	defer func() {
		ref.Release(context.TODO())
	}()

	immutable, ok := toImmutableRef(ref)
	if !ok {
		return errors.Errorf("invalid reference for exporting: %T", ref)
	}
	if err := immutable.Finalize(ctx); err != nil {
		return err
	}

	if exp != nil {
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

// getCacheKey return a cache key for a single output of a vertex
func (s *Solver) getCacheKey(ctx context.Context, g *vertex, inputs []digest.Digest, index Index) (dgst digest.Digest, retErr error) {
	state, err := s.activeState.vertexState(ctx, g.digest, func() (Op, error) {
		return s.resolve(g)
	})
	if err != nil {
		return "", err
	}

	pw, _, ctx := progress.FromContext(ctx, progress.WithMetadata("vertex", g.Digest()))
	defer pw.Close()

	if len(g.inputs) == 0 {
		g.notifyStarted(ctx)
		defer func() {
			g.notifyCompleted(ctx, false, retErr)
		}()
	}

	dgst, err = state.GetCacheKey(ctx, func(ctx context.Context, op Op) (digest.Digest, error) {
		return op.CacheKey(ctx)
	})

	if err != nil {
		return "", err
	}

	dt, err := json.Marshal(struct {
		Index  Index
		Inputs []digest.Digest
		Digest digest.Digest
	}{
		Index:  index,
		Inputs: inputs,
		Digest: dgst,
	})
	if err != nil {
		return "", err
	}
	return digest.FromBytes(dt), nil
}

// walkVertex walks all possible cache keys and a evaluated reference for a
// single output of a vertex.
func (s *Solver) walkVertex(ctx context.Context, g *vertex, index Index, fn func(digest.Digest, Reference) (bool, error)) (retErr error) {
	state, err := s.activeState.vertexState(ctx, g.digest, func() (Op, error) {
		return s.resolve(g)
	})
	if err != nil {
		return err
	}

	inputCacheKeysMu := sync.Mutex{}
	inputCacheKeys := make([][]digest.Digest, len(g.inputs))
	walkerStopped := false

	inputCtx, cancelInputCtx := context.WithCancel(ctx)
	defer cancelInputCtx()

	inputRefs := make([]Reference, len(g.inputs))

	defer func() {
		for _, r := range inputRefs {
			if r != nil {
				go r.Release(context.TODO())
			}
		}
	}()

	if len(g.inputs) > 0 {
		eg, ctx := errgroup.WithContext(ctx)
		for i, in := range g.inputs {
			func(i int, in *input) {
				eg.Go(func() error {
					var inputRef Reference
					defer func() {
						if inputRef != nil {
							go inputRef.Release(context.TODO())
						}
					}()
					err := s.walkVertex(inputCtx, in.vertex, in.index, func(k digest.Digest, ref Reference) (bool, error) {
						if k == "" && ref == nil {
							// indicator between cache key and reference
							if inputRef != nil {
								return true, nil
							}
							// TODO: might be good to block here if other inputs may
							// cause cache hits to avoid duplicate work.
							return false, nil
						}
						if ref != nil {
							inputRef = ref
							return true, nil
						}

						inputCacheKeysMu.Lock()
						defer inputCacheKeysMu.Unlock()

						if walkerStopped {
							return walkerStopped, nil
						}

						// try all known combinations together with new key
						inputCacheKeysCopy := append([][]digest.Digest{}, inputCacheKeys...)
						inputCacheKeysCopy[i] = []digest.Digest{k}
						inputCacheKeys[i] = append(inputCacheKeys[i], k)

						for _, inputKeys := range combinations(inputCacheKeysCopy) {
							cacheKey, err := s.getCacheKey(ctx, g, inputKeys, index)
							if err != nil {
								return false, err
							}
							stop, err := fn(cacheKey, nil)
							if err != nil {
								return false, err
							}
							if stop {
								walkerStopped = true
								cancelInputCtx() // parent matched, stop processing current node and its inputs
								return true, nil
							}
						}

						// if no parent matched, try looking up current node from cache
						if s.cache != nil && inputRef == nil {
							lookupRef, err := s.cache.Lookup(ctx, k)
							if err != nil {
								return false, err
							}
							if lookupRef != nil {
								inputRef = lookupRef.(Reference)
								in.vertex.recursiveMarkCached(ctx)
								return true, nil
							}
						}
						return false, nil
					})
					if inputRef != nil {
						// make sure that the inputs for other steps don't get released on cancellation
						if ref, ok := toImmutableRef(inputRef); ok {
							if err := cache.CachePolicyRetain(ref); err != nil {
								return err
							}
							if err := ref.Metadata().Commit(); err != nil {
								return err
							}
						}
					}
					inputCacheKeysMu.Lock()
					defer inputCacheKeysMu.Unlock()
					if walkerStopped {
						return nil
					}
					if err != nil {
						return err
					}
					inputRefs[i] = inputRef
					inputRef = nil
					return nil
				})
			}(i, in)
		}
		if err := eg.Wait(); err != nil && !walkerStopped {
			return err
		}
	} else {
		cacheKey, err := s.getCacheKey(ctx, g, nil, index)
		if err != nil {
			return err
		}
		stop, err := fn(cacheKey, nil)
		if err != nil {
			return err
		}
		walkerStopped = stop
	}

	if walkerStopped {
		return nil
	}

	var contentKeys []digest.Digest
	if s.cache != nil {
		// try to determine content based key
		contentKeys, err = state.op.ContentKeys(ctx, combinations(inputCacheKeys), inputRefs)
		if err != nil {
			return err
		}

		for _, k := range contentKeys {
			cks, err := s.cache.GetContentMapping(contentKeyWithIndex(k, index))
			if err != nil {
				return err
			}
			for _, k := range cks {
				stop, err := fn(k, nil)
				if err != nil {
					return err
				}
				if stop {
					return nil
				}
			}
		}
	}

	// signal that no more cache keys are coming
	stop, err := fn("", nil)
	if err != nil {
		return err
	}
	if stop {
		return nil
	}

	pw, _, ctx := progress.FromContext(ctx, progress.WithMetadata("vertex", g.Digest()))
	defer pw.Close()

	g.notifyStarted(ctx)
	defer func() {
		g.notifyCompleted(ctx, false, retErr)
	}()

	ref, err := state.GetRefs(ctx, index, func(ctx context.Context, op Op) ([]Reference, error) {
		refs, err := op.Run(ctx, inputRefs)
		if err != nil {
			return nil, err
		}
		if s.cache != nil {
			mainInputKeys := firstKeys(inputCacheKeys)
			for i, ref := range refs {
				if ref != nil {
					cacheKey, err := s.getCacheKey(ctx, g, mainInputKeys, Index(i))
					if err != nil {
						return nil, err
					}
					r := originRef(ref)
					if err := s.cache.Set(cacheKey, r); err != nil {
						logrus.Errorf("failed to save cache for %s: %v", cacheKey, err)
					}

					for _, k := range contentKeys {
						if err := s.cache.SetContentMapping(contentKeyWithIndex(k, Index(i)), r); err != nil {
							logrus.Errorf("failed to save content mapping: %v", err)
						}
					}
				}
			}
		}
		return refs, nil
	})
	if err != nil {
		return err
	}

	// return reference
	_, err = fn("", ref)
	if err != nil {
		return err
	}

	return nil
}

func (s *Solver) getRef(ctx context.Context, g *vertex, index Index) (ref Reference, retErr error) {
	logrus.Debugf("> getRef %s %v %s", g.digest, index, g.name)
	defer logrus.Debugf("< getRef %s %v", g.digest, index)

	var returnRef Reference
	err := s.walkVertex(ctx, g, index, func(ck digest.Digest, ref Reference) (bool, error) {
		if ref != nil {
			returnRef = ref
			return true, nil
		}
		if ck == "" {
			return false, nil
		}
		lookupRef, err := s.cache.Lookup(ctx, ck)
		if err != nil {
			return false, err
		}
		if lookupRef != nil {
			g.recursiveMarkCached(ctx)
			returnRef = lookupRef.(Reference)
			return true, nil
		}
		return false, nil
	})
	if err != nil {
		return nil, err
	}

	return returnRef, nil
}

func firstKeys(inp [][]digest.Digest) []digest.Digest {
	var out []digest.Digest
	for _, v := range inp {
		out = append(out, v[0])
	}
	return out
}

func combinations(inp [][]digest.Digest) [][]digest.Digest {
	var out [][]digest.Digest
	if len(inp) == 0 {
		return inp
	}
	if len(inp) == 1 {
		for _, v := range inp[0] {
			out = append(out, []digest.Digest{v})
		}
		return out
	}
	for _, v := range inp[0] {
		for _, v2 := range combinations(inp[1:]) {
			out = append(out, append([]digest.Digest{v}, v2...))
		}
	}
	return out
}

func contentKeyWithIndex(dgst digest.Digest, index Index) digest.Digest {
	return digest.FromBytes([]byte(fmt.Sprintf("%s.%d", dgst, index)))
}
