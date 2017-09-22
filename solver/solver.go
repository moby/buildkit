package solver

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/exporter"
	"github.com/moby/buildkit/frontend"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/source"
	"github.com/moby/buildkit/util/bgfunc"
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
	ImageSource      source.Source
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
			return nil, nil
		}
	}, opt.InstructionCache, opt.ImageSource)
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
	Probe(ctx context.Context, key digest.Digest) (bool, error)
	Lookup(ctx context.Context, key digest.Digest) (interface{}, error) // TODO: regular ref
	Set(key digest.Digest, ref interface{}) error
	SetContentMapping(contentKey, key digest.Digest) error
	GetContentMapping(dgst digest.Digest) ([]digest.Digest, error)
}

type Solver struct {
	resolve     ResolveOpFunc
	jobs        *jobList
	cache       InstructionCache
	imageSource source.Source
}

func New(resolve ResolveOpFunc, cache InstructionCache, imageSource source.Source) *Solver {
	return &Solver{resolve: resolve, jobs: newJobList(), cache: cache, imageSource: imageSource}
}

func (s *Solver) Solve(ctx context.Context, id string, f frontend.Frontend, v Vertex, exp exporter.ExporterInstance, frontendOpt map[string]string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	pr, ctx, closeProgressWriter := progress.NewContext(ctx)

	defer closeProgressWriter()

	var vv *vertex
	var index Index
	if v != nil {
		if len(v.Inputs()) == 0 {
			return errors.New("required vertex needs to have inputs")
		}

		index = v.Inputs()[0].Index
		v = v.Inputs()[0].Vertex

		vv = toInternalVertex(v)
	}

	ctx, j, err := s.jobs.new(ctx, id, pr, s.cache)
	if err != nil {
		return err
	}

	var ref Reference
	var exporterOpt map[string]interface{}
	// solver:             s.getRef,
	if vv != nil {
		if err := j.load(vv, s.resolve); err != nil {
			j.discard()
			return err
		}
		ref, err = j.getRef(ctx, vv, index)
	} else {
		ref, exporterOpt, err = f.Solve(ctx, &llbBridge{
			job:                j,
			resolveOp:          s.resolve,
			resolveImageConfig: s.imageSource.(resolveImageConfig),
		}, frontendOpt)
	}
	j.discard()
	if err != nil {
		return err
	}

	defer func() {
		go ref.Release(context.TODO())
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
		err := exp.Export(ctx, immutable, exporterOpt)
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

func (s *Solver) loadAndSolveChildVertex(ctx context.Context, dgst digest.Digest, vv *vertex, index Index) (Reference, error) {
	return s.jobs.loadAndSolveChildVertex(ctx, dgst, vv, index, s.resolve, s.cache)
}

type VertexSolver interface {
	CacheKey(ctx context.Context, index Index) (digest.Digest, error)
	OutputEvaluator(Index) (VertexEvaluator, error)
	Release() error
}

type vertexInput struct {
	solver    VertexSolver
	ev        VertexEvaluator
	cacheKeys []digest.Digest
	ref       Reference
}

type vertexSolver struct {
	inputs []*vertexInput
	v      *vertex
	op     Op
	cache  InstructionCache
	refs   []*sharedRef
	f      *bgfunc.F
	ctx    context.Context

	baseKey digest.Digest
	mu      sync.Mutex
	results []digest.Digest

	signal *signal // used to notify that there are callers who need more data
}

type resolveF func(digest.Digest) (VertexSolver, error)

func newVertexSolver(ctx context.Context, v *vertex, op Op, c InstructionCache, resolve resolveF) (VertexSolver, error) {
	inputs := make([]*vertexInput, len(v.inputs))
	for i, in := range v.inputs {
		s, err := resolve(in.vertex.digest)
		if err != nil {
			return nil, err
		}
		ev, err := s.OutputEvaluator(in.index)
		if err != nil {
			return nil, err
		}
		inputs[i] = &vertexInput{
			solver: s,
			ev:     ev,
		}
	}
	return &vertexSolver{
		ctx:    ctx,
		inputs: inputs,
		v:      v,
		op:     op,
		cache:  c,
		signal: newSignaller(),
	}, nil
}

func (vs *vertexSolver) CacheKey(ctx context.Context, index Index) (digest.Digest, error) {
	vs.mu.Lock()
	defer vs.mu.Unlock()
	if vs.baseKey == "" {
		eg, ctx := errgroup.WithContext(vs.ctx)
		for i := range vs.inputs {
			func(i int) {
				eg.Go(func() error {
					k, err := vs.inputs[i].solver.CacheKey(ctx, vs.v.inputs[i].index)
					if err != nil {
						return err
					}
					vs.inputs[i].cacheKeys = append(vs.inputs[i].cacheKeys, k)
					return nil
				})
			}(i)
		}
		var dgst digest.Digest
		eg.Go(func() error {
			var err error
			dgst, err = vs.op.CacheKey(ctx)
			if err != nil {
				return err
			}
			return nil
		})

		if err := eg.Wait(); err != nil {
			return "", err
		}

		vs.baseKey = dgst
	}

	k, err := vs.lastCacheKey()
	if err != nil {
		return "", err
	}

	return cacheKeyForIndex(k, index), nil
}

func (vs *vertexSolver) lastCacheKey() (digest.Digest, error) {
	return vs.currentCacheKey(true)
}

func (vs *vertexSolver) mainCacheKey() (digest.Digest, error) {
	return vs.currentCacheKey(false)
}

func (vs *vertexSolver) currentCacheKey(last bool) (digest.Digest, error) {
	inputKeys := make([]digest.Digest, len(vs.inputs))
	for i, inp := range vs.inputs {
		if len(inp.cacheKeys) == 0 {
			return "", errors.Errorf("inputs not processed")
		}
		if last {
			inputKeys[i] = inp.cacheKeys[len(inp.cacheKeys)-1]
		} else {
			inputKeys[i] = inp.cacheKeys[0]
		}
	}
	dt, err := json.Marshal(struct {
		Inputs   []digest.Digest
		CacheKey digest.Digest
	}{Inputs: inputKeys, CacheKey: vs.baseKey})
	if err != nil {
		return "", err
	}
	return digest.FromBytes(dt), nil
}

func (vs *vertexSolver) OutputEvaluator(index Index) (VertexEvaluator, error) {
	if vs.f == nil {
		f, err := bgfunc.New(vs.ctx, vs.run)
		if err != nil {
			return nil, err
		}
		vs.f = f
	}
	c := vs.f.NewCaller()
	ve := &vertexEvaluator{vertexSolver: vs, c: c, index: index}
	return ve, nil
}

func (vs *vertexSolver) Release() error {
	for _, inp := range vs.inputs {
		if inp.ref != nil {
			inp.ref.Release(context.TODO())
		}
	}
	if vs.refs != nil {
		for _, r := range vs.refs {
			r.Release(context.TODO())
		}
	}
	return nil
}

// run is called by the bgfunc concurrency primitive. This function may be
// called multiple times but never in parallal. Repeated calls should make an
// effort to continue from previous state. Lock vs.mu to syncronize data to the
// callers. Signal parameter can be used to notify callers that new data is
// available without returning from the function.
func (vs *vertexSolver) run(ctx context.Context, signal func()) (retErr error) {
	vs.mu.Lock()
	if vs.refs != nil {
		vs.mu.Unlock()
		return nil
	}
	vs.mu.Unlock()

	wait := vs.signal.Wait()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-wait:
	}

	// this is where you lookup the cache keys that were successfully probed

	eg, ctx2 := errgroup.WithContext(ctx)

	// process all the inputs
	for i, inp := range vs.inputs {
		if inp.ref == nil {
			func(i int) {
				eg.Go(func() error {
					inp := vs.inputs[i]
					defer inp.ev.Cancel()
					for {

						select {
						case <-ctx2.Done():
							return ctx2.Err()
						case <-wait:
						}

						// check if current cache key is in cache
						if len(inp.cacheKeys) > 0 {
							ref, err := vs.cache.Lookup(ctx2, inp.cacheKeys[len(inp.cacheKeys)-1])
							if err != nil {
								return err
							}
							if ref != nil {
								inp.ref = ref.(Reference)
								return nil
							}
						}

						// evaluate next cachekey/reference for input
						res, err := inp.ev.Next(ctx2)
						if err != nil {
							return err
						}
						if res == nil { // there is no more data coming
							return nil
						}
						if ref := res.Reference; ref != nil {
							if ref, ok := toImmutableRef(ref); ok {
								if !cache.HasCachePolicyRetain(ref) {
									if err := cache.CachePolicyRetain(ref); err != nil {
										return err
									}
									ref.Metadata().Commit()
								}
								inp.ref = ref
							}
							return nil
						}

						// Only try matching cache if the cachekey for input is present
						exists, err := vs.cache.Probe(ctx2, res.CacheKey)
						if err != nil {
							return err
						}
						if exists {
							vs.mu.Lock()
							inp.cacheKeys = append(inp.cacheKeys, res.CacheKey)
							dgst, err := vs.lastCacheKey()
							if err != nil {
								vs.mu.Unlock()
								return err
							}
							vs.results = append(vs.results, dgst)
							signal()                 // wake up callers
							wait = vs.signal.Reset() // make sure we don't continue unless there are callers
							vs.mu.Unlock()
						}
					}
				})
			}(i)
		}
	}

	if err := eg.Wait(); err != nil {
		return err
	}

	// Find extra cache keys by content
	inputRefs := make([]Reference, len(vs.inputs))
	lastInputKeys := make([]digest.Digest, len(vs.inputs))
	for i := range vs.inputs {
		inputRefs[i] = vs.inputs[i].ref
		lastInputKeys[i] = vs.inputs[i].cacheKeys[len(vs.inputs[i].cacheKeys)-1]
	}

	// TODO: avoid doing this twice on cancellation+resume
	contentKeys, err := vs.op.ContentKeys(ctx, [][]digest.Digest{lastInputKeys}, inputRefs)
	if err != nil {
		return err
	}

	var extraKeys []digest.Digest
	for _, k := range contentKeys {
		cks, err := vs.cache.GetContentMapping(k)
		if err != nil {
			return err
		}
		extraKeys = append(extraKeys, cks...)
	}
	if len(extraKeys) > 0 {
		vs.mu.Lock()
		vs.results = append(vs.results, extraKeys...)
		signal()
		wait = vs.signal.Reset()
		vs.mu.Unlock()
	}

	select {
	case <-ctx.Done():
		return
	case <-wait:
	}

	// no cache hit. start evaluating the node
	vs.v.notifyStarted(ctx)
	defer func() {
		vs.v.notifyCompleted(ctx, false, retErr)
	}()

	refs, err := vs.op.Run(ctx, inputRefs)
	if err != nil {
		return err
	}
	sr := make([]*sharedRef, len(refs))
	for i, r := range refs {
		sr[i] = newSharedRef(r)
	}
	vs.refs = sr

	// store the cacheKeys for current refs
	if vs.cache != nil {
		cacheKey, err := vs.mainCacheKey()
		if err != nil {
			return err
		}
		for i, ref := range refs {
			if err != nil {
				return err
			}
			r := originRef(ref)
			if err := vs.cache.Set(cacheKeyForIndex(cacheKey, Index(i)), r); err != nil {
				logrus.Errorf("failed to save cache for %s: %v", cacheKey, err)
			}
		}
		if len(contentKeys) > 0 {
			for _, ck := range contentKeys {
				if err := vs.cache.SetContentMapping(ck, cacheKey); err != nil {
					logrus.Errorf("failed to save content mapping: %v", err)
				}
			}
		}
	}
	return nil
}

type VertexEvaluator interface {
	Next(context.Context) (*VertexResult, error)
	Cancel() error
}

type vertexEvaluator struct {
	*vertexSolver
	c      *bgfunc.Caller
	cursor int
	index  Index
}

func (ve *vertexEvaluator) Next(ctx context.Context) (*VertexResult, error) {
	v, err := ve.c.Call(ctx, func() (interface{}, error) {
		ve.mu.Lock()
		defer ve.mu.Unlock()
		if ve.refs != nil {
			return &VertexResult{Reference: ve.refs[int(ve.index)].Clone()}, nil
		}
		if i := ve.cursor; i < len(ve.results) {
			ve.cursor++
			return &VertexResult{CacheKey: cacheKeyForIndex(ve.results[i], ve.index)}, nil
		}
		ve.signal.Signal()
		return nil, nil
	})
	if err != nil {
		return nil, err
	}
	if v == nil {
		return nil, nil // no more records are coming
	}
	return v.(*VertexResult), nil
}

func (ve *vertexEvaluator) Cancel() error {
	return ve.c.Cancel()
}

type VertexResult struct {
	CacheKey  digest.Digest
	Reference Reference
}

// llbBridge is an helper used by frontends
type llbBridge struct {
	resolveImageConfig
	job       *job
	resolveOp ResolveOpFunc
}

type resolveImageConfig interface {
	ResolveImageConfig(ctx context.Context, ref string) (digest.Digest, []byte, error)
}

func (s *llbBridge) Solve(ctx context.Context, dt [][]byte) (cache.ImmutableRef, error) {
	v, err := LoadLLB(dt)
	if err != nil {
		return nil, err
	}
	if len(v.Inputs()) == 0 {
		return nil, errors.New("required vertex needs to have inputs")
	}

	index := v.Inputs()[0].Index
	v = v.Inputs()[0].Vertex

	vv := toInternalVertex(v)

	if err := s.job.load(vv, s.resolveOp); err != nil {
		return nil, err
	}
	ref, err := s.job.getRef(ctx, vv, index)
	if err != nil {
		return nil, err
	}
	immutable, ok := toImmutableRef(ref)
	if !ok {
		return nil, errors.Errorf("invalid reference for exporting: %T", ref)
	}

	return immutable, nil
}

func cacheKeyForIndex(dgst digest.Digest, index Index) digest.Digest {
	return digest.FromBytes([]byte(fmt.Sprintf("%s.%d", dgst, index)))
}
