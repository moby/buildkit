package solver

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/cache/cacheimport"
	"github.com/moby/buildkit/cache/contenthash"
	"github.com/moby/buildkit/cache/instructioncache"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/exporter"
	"github.com/moby/buildkit/frontend"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/bgfunc"
	"github.com/moby/buildkit/util/progress"
	"github.com/moby/buildkit/worker"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"golang.org/x/net/context"
	"golang.org/x/sync/errgroup"
)

// DetermineVertexWorker determines worker for a vertex.
// Currently, constraint is just ignored.
// Also we need to track the workers of the inputs.
func DetermineVertexWorker(wc *worker.Controller, v Vertex) (*worker.Worker, error) {
	// TODO: multiworker
	return wc.GetDefault()
}

func NewLLBSolver(wc *worker.Controller, frontends map[string]frontend.Frontend) *Solver {
	var s *Solver
	s = New(func(v Vertex) (Op, error) {
		w, err := DetermineVertexWorker(wc, v)
		if err != nil {
			return nil, err
		}
		switch op := v.Sys().(type) {
		case *pb.Op_Source:
			return newSourceOp(v, op, w.SourceManager)
		case *pb.Op_Exec:
			return newExecOp(v, op, w.CacheManager, w.Executor)
		case *pb.Op_Build:
			return newBuildOp(v, op, s)
		default:
			return nil, nil
		}
	}, wc, frontends)
	return s
}

// ResolveOpFunc finds an Op implementation for a vertex
type ResolveOpFunc func(Vertex) (Op, error)

// Reference is a reference to the object passed through the build steps.
// This interface is a subset of the cache.Ref interface.
// For ease of unit testing, this interface only has Release().
type Reference interface {
	Release(context.Context) error
}

// Op is an implementation for running a vertex
type Op interface {
	// CacheKey returns a persistent cache key for operation.
	CacheKey(context.Context) (digest.Digest, error)
	// ContentMask returns a partial cache checksum with content paths to the
	// inputs. User can combine the content checksum of these paths to get a valid
	// content based cache key.
	ContentMask(context.Context) (digest.Digest, [][]string, error)
	// Run runs an operation and returns the output references.
	Run(ctx context.Context, inputs []Reference) (outputs []Reference, err error)
}

type Solver struct {
	resolve          ResolveOpFunc
	jobs             *jobList
	workerController *worker.Controller
	frontends        map[string]frontend.Frontend
}

func New(resolve ResolveOpFunc, wc *worker.Controller, f map[string]frontend.Frontend) *Solver {
	return &Solver{resolve: resolve, jobs: newJobList(), workerController: wc, frontends: f}
}

type SolveRequest struct {
	Definition     *pb.Definition
	Frontend       frontend.Frontend
	Exporter       exporter.ExporterInstance
	FrontendOpt    map[string]string
	ExportCacheRef string
	ImportCacheRef string
}

func (s *Solver) solve(ctx context.Context, j *job, req SolveRequest) (Reference, map[string][]byte, error) {
	if req.Definition == nil {
		if req.Frontend == nil {
			return nil, nil, errors.Errorf("invalid request: no definition nor frontend")
		}
		return req.Frontend.Solve(ctx, s.llbBridge(j), req.FrontendOpt)
	}

	inp, err := j.load(req.Definition, s.resolve)
	if err != nil {
		return nil, nil, err
	}
	ref, err := j.getRef(ctx, inp.Vertex.(*vertex).clientVertex, inp.Index)
	return ref, nil, err
}

func (s *Solver) llbBridge(j *job) *llbBridge {
	// FIXME(AkihiroSuda): make sure worker implements interfaces required by llbBridge
	worker, err := s.workerController.GetDefault()
	if err != nil {
		panic(err)
	}
	return &llbBridge{job: j, Solver: s, worker: worker}
}

func (s *Solver) Solve(ctx context.Context, id string, req SolveRequest) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	pr, ctx, closeProgressWriter := progress.NewContext(ctx)
	defer closeProgressWriter()

	// TODO: multiworker
	defaultWorker, err := s.workerController.GetDefault()
	if err != nil {
		return err
	}
	if importRef := req.ImportCacheRef; importRef != "" {
		cache, err := defaultWorker.CacheImporter.Import(ctx, importRef)
		if err != nil {
			return err
		}
		defaultWorker.InstructionCache = instructioncache.Union(defaultWorker.InstructionCache, cache)
	}

	// register a build job. vertex needs to be loaded to a job to run
	ctx, j, err := s.jobs.new(ctx, id, pr, defaultWorker.InstructionCache)
	if err != nil {
		return err
	}

	ref, exporterOpt, err := s.solve(ctx, j, req)
	defer j.discard()
	if err != nil {
		return err
	}

	defer func() {
		if ref != nil {
			go ref.Release(context.TODO())
		}
	}()

	var immutable cache.ImmutableRef
	if ref != nil {
		var ok bool
		immutable, ok = toImmutableRef(ref)
		if !ok {
			return errors.Errorf("invalid reference for exporting: %T", ref)
		}
		if err := immutable.Finalize(ctx); err != nil {
			return err
		}
	}

	if exp := req.Exporter; exp != nil {
		if err := inVertexContext(ctx, exp.Name(), func(ctx context.Context) error {
			return exp.Export(ctx, immutable, exporterOpt)
		}); err != nil {
			return err
		}
	}

	if exportName := req.ExportCacheRef; exportName != "" {
		if err := inVertexContext(ctx, "exporting build cache", func(ctx context.Context) error {
			cache, err := j.cacheExporter(ref)
			if err != nil {
				return err
			}

			records, err := cache.Export(ctx)
			if err != nil {
				return err
			}

			// TODO: multiworker
			return defaultWorker.CacheExporter.Export(ctx, records, exportName)
		}); err != nil {
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

func (s *Solver) subBuild(ctx context.Context, dgst digest.Digest, req SolveRequest) (Reference, error) {
	jl := s.jobs
	jl.mu.Lock()
	st, ok := jl.actives[dgst]
	if !ok {
		jl.mu.Unlock()
		return nil, errors.Errorf("no such parent vertex: %v", dgst)
	}

	var inp *Input
	for j := range st.jobs {
		var err error
		inp, err = j.loadInternal(req.Definition, s.resolve)
		if err != nil {
			jl.mu.Unlock()
			return nil, err
		}
	}
	st = jl.actives[inp.Vertex.Digest()]
	jl.mu.Unlock()

	w, err := DetermineVertexWorker(s.workerController, inp.Vertex)
	if err != nil {
		return nil, err
	}
	return getRef(ctx, st.solver, inp.Vertex.(*vertex).clientVertex, inp.Index, w.InstructionCache) // TODO: combine to pass single input                                        // TODO: export cache for subbuilds
}

type VertexSolver interface {
	CacheKey(ctx context.Context, index Index) (digest.Digest, error)
	OutputEvaluator(Index) (VertexEvaluator, error)
	Release() error
	Cache(Index, Reference) CacheExporter
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
	cv     client.Vertex
	op     Op
	cache  instructioncache.InstructionCache
	refs   []*sharedRef
	f      *bgfunc.F
	ctx    context.Context

	baseKey        digest.Digest
	mu             sync.Mutex
	results        []digest.Digest
	markCachedOnce sync.Once
	contentKey     digest.Digest

	signal *signal // used to notify that there are callers who need more data
}

type resolveF func(digest.Digest) (VertexSolver, error)

func newVertexSolver(ctx context.Context, v *vertex, op Op, c instructioncache.InstructionCache, resolve resolveF) (*vertexSolver, error) {
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
		ev.Cancel()
		inputs[i] = &vertexInput{
			solver: s,
			ev:     ev,
		}
	}
	return &vertexSolver{
		ctx:    ctx,
		inputs: inputs,
		v:      v,
		cv:     v.clientVertex,
		op:     op,
		cache:  c,
		signal: newSignaller(),
	}, nil
}

func markCached(ctx context.Context, cv client.Vertex) {
	pw, _, _ := progress.FromContext(ctx)
	defer pw.Close()

	if cv.Started == nil {
		now := time.Now()
		cv.Started = &now
		cv.Completed = &now
		cv.Cached = true
	}
	pw.Write(cv.Digest.String(), cv)
}

type CacheExporter interface {
	Export(context.Context) ([]cacheimport.CacheRecord, error)
}

func (vs *vertexSolver) Cache(index Index, ref Reference) CacheExporter {
	return &cacheExporter{vertexSolver: vs, index: index, ref: ref}
}

type cacheExporter struct {
	*vertexSolver
	index Index
	ref   Reference
}

func (ce *cacheExporter) Export(ctx context.Context) ([]cacheimport.CacheRecord, error) {
	return ce.vertexSolver.Export(ctx, ce.index, ce.ref)
}

func (vs *vertexSolver) Export(ctx context.Context, index Index, ref Reference) ([]cacheimport.CacheRecord, error) {
	mp := map[digest.Digest]cacheimport.CacheRecord{}
	if err := vs.appendInputCache(ctx, mp); err != nil {
		return nil, err
	}
	dgst, err := vs.mainCacheKey()
	if err != nil {
		return nil, err
	}
	immutable, ok := toImmutableRef(ref)
	if !ok {
		return nil, errors.Errorf("invalid reference")
	}
	dgst = cacheKeyForIndex(dgst, index)
	mp[dgst] = cacheimport.CacheRecord{CacheKey: dgst, Reference: immutable}
	out := make([]cacheimport.CacheRecord, 0, len(mp))
	for _, cr := range mp {
		out = append(out, cr)
	}
	return out, nil
}

func (vs *vertexSolver) appendInputCache(ctx context.Context, mp map[digest.Digest]cacheimport.CacheRecord) error {
	for i, inp := range vs.inputs {
		mainDgst, err := inp.solver.(*vertexSolver).mainCacheKey()
		if err != nil {
			return err
		}
		dgst := cacheKeyForIndex(mainDgst, vs.v.inputs[i].index)
		if cr, ok := mp[dgst]; !ok || (cr.Reference == nil && inp.ref != nil) {
			if err := inp.solver.(*vertexSolver).appendInputCache(ctx, mp); err != nil {
				return err
			}
			if inp.ref != nil && len(inp.solver.(*vertexSolver).inputs) > 0 { // Ignore pushing the refs for sources
				ref, ok := toImmutableRef(inp.ref)
				if !ok {
					return errors.Errorf("invalid reference")
				}
				mp[dgst] = cacheimport.CacheRecord{CacheKey: dgst, Reference: ref}
			} else {
				mp[dgst] = cacheimport.CacheRecord{CacheKey: dgst}
			}
		}
	}
	if ck := vs.contentKey; ck != "" {
		mainDgst, err := vs.mainCacheKey()
		if err != nil {
			return err
		}
		mp[ck] = cacheimport.CacheRecord{CacheKey: mainDgst, ContentKey: ck}
	}
	return nil
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

	waitFirst := vs.signal.Wait()
	waitRun := waitFirst

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-waitFirst:
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

					waitNext := waitFirst
					for {
						select {
						case <-ctx2.Done():
							return ctx2.Err()
						case <-waitNext:
						}

						// check if current cache key is in cache
						if len(inp.cacheKeys) > 0 {
							ref, err := vs.cache.Lookup(ctx2, inp.cacheKeys[len(inp.cacheKeys)-1], inp.solver.(*vertexSolver).v.Name())
							if err != nil {
								return err
							}
							if ref != nil {
								inp.ref = ref.(Reference)
								inp.solver.(*vertexSolver).markCachedOnce.Do(func() {
									markCached(ctx, inp.solver.(*vertexSolver).cv)
								})
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
							signal()                     // wake up callers
							waitNext = vs.signal.Reset() // make sure we don't continue unless there are callers
							waitRun = waitNext
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

	dgst, inp, err := vs.op.ContentMask(ctx)
	if err != nil {
		return err
	}

	var contentKey digest.Digest
	if dgst != "" {
		contentKey, err = calculateContentHash(ctx, inputRefs, dgst, lastInputKeys, inp)
		if err != nil {
			return err
		}
		vs.contentKey = contentKey

		var extraKeys []digest.Digest
		cks, err := vs.cache.GetContentMapping(contentKey)
		if err != nil {
			return err
		}
		extraKeys = append(extraKeys, cks...)
		if len(extraKeys) > 0 {
			vs.mu.Lock()
			vs.results = append(vs.results, extraKeys...)
			signal()
			waitRun = vs.signal.Reset()
			vs.mu.Unlock()
		}
	}

	select {
	case <-ctx.Done():
		return
	case <-waitRun:
	}

	// no cache hit. start evaluating the node
	notifyStarted(ctx, &vs.cv)
	defer func() {
		notifyCompleted(ctx, &vs.cv, retErr)
	}()

	refs, err := vs.op.Run(ctx, inputRefs)
	if err != nil {
		return err
	}
	sr := make([]*sharedRef, len(refs))
	for i, r := range refs {
		sr[i] = newSharedRef(r)
	}
	vs.mu.Lock()
	vs.refs = sr
	vs.mu.Unlock()

	// store the cacheKeys for current refs
	if vs.cache != nil {
		cacheKey, err := vs.lastCacheKey()
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
		if contentKey != "" {
			if err := vs.cache.SetContentMapping(contentKey, cacheKey); err != nil {
				logrus.Errorf("failed to save content mapping: %v", err)
			}
		}
	}
	return nil
}

func getInputContentHash(ctx context.Context, ref cache.ImmutableRef, selectors []string) (digest.Digest, error) {
	out := make([]digest.Digest, 0, len(selectors))
	for _, s := range selectors {
		dgst, err := contenthash.Checksum(ctx, ref, s)
		if err != nil {
			return "", err
		}
		out = append(out, dgst)
	}
	if len(out) == 1 {
		return out[0], nil
	}
	dt, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return digest.FromBytes(dt), nil
}

func calculateContentHash(ctx context.Context, refs []Reference, mainDigest digest.Digest, inputs []digest.Digest, contentMap [][]string) (digest.Digest, error) {
	dgsts := make([]digest.Digest, len(contentMap))
	eg, ctx := errgroup.WithContext(ctx)
	for i, sel := range contentMap {
		if sel == nil {
			dgsts[i] = inputs[i]
			continue
		}
		func(i int) {
			eg.Go(func() error {
				ref, ok := toImmutableRef(refs[i])
				if !ok {
					return errors.Errorf("invalid reference for exporting: %T", ref)
				}
				dgst, err := getInputContentHash(ctx, ref, contentMap[i])
				if err != nil {
					return err
				}
				dgsts[i] = dgst
				return nil
			})
		}(i)
	}
	if err := eg.Wait(); err != nil {
		return "", err
	}
	dt, err := json.Marshal(struct {
		Main   digest.Digest
		Inputs []digest.Digest
	}{
		Main:   mainDigest,
		Inputs: dgsts,
	})
	if err != nil {
		return "", err
	}
	return digest.FromBytes(dt), nil
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

func cacheKeyForIndex(dgst digest.Digest, index Index) digest.Digest {
	return digest.FromBytes([]byte(fmt.Sprintf("%s.%d", dgst, index)))
}
