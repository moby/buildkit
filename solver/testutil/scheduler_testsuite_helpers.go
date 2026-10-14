package testutil

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"sync/atomic"
	"time"

	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/solver"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
)

func generateSubGraph(nodes int) (solver.Edge, int) {
	if nodes == 1 {
		value := rand.Int() % 500 //nolint:gosec
		return solver.Edge{Vertex: vtxConst(value, vtxOpt{})}, value
	}
	spread := rand.Int()%5 + 2 //nolint:gosec
	inc := min(int(math.Ceil(float64(nodes)/float64(spread))), nodes)
	added := 1
	value := 0
	inputs := []solver.Edge{}
	i := 0
	for {
		i++
		if added >= nodes {
			break
		}
		if added+inc > nodes {
			inc = nodes - added
		}
		e, v := generateSubGraph(inc)
		inputs = append(inputs, e)
		value += v
		added += inc
	}
	extra := rand.Int() % 500 //nolint:gosec
	value += extra
	return solver.Edge{Vertex: vtxSum(extra, vtxOpt{inputs: inputs})}, value
}

type vtxOpt struct {
	name             string
	cacheKeySeed     string
	cacheKeySeeds    []func() string
	execDelay        time.Duration
	cacheDelay       time.Duration
	cachePreFunc     func(context.Context) error
	execPreFunc      func(context.Context) error
	inputs           []solver.Edge
	value            string
	result           solver.Result
	slowCacheCompute map[int]solver.ResultBasedCacheFunc
	selectors        map[int]digest.Digest
	cacheSource      solver.CacheManager
	ignoreCache      bool
}

func vtx(opt vtxOpt) *vertex {
	if opt.name == "" {
		opt.name = identity.NewID()
	}
	if opt.cacheKeySeed == "" {
		opt.cacheKeySeed = identity.NewID()
	}
	return &vertex{opt: opt}
}

type vertex struct {
	opt vtxOpt

	cacheCallCount *int64
	execCallCount  *int64
}

type invalidCacheMapVertex struct {
	*vertex
	depCount int
}

func (v *invalidCacheMapVertex) Sys() any {
	return v
}

func (v *invalidCacheMapVertex) CacheMap(context.Context, solver.JobContext, int) (*solver.CacheMap, bool, error) {
	return &solver.CacheMap{
		Digest: digest.FromBytes([]byte(v.Name())),
		Deps: make([]struct {
			Selector          digest.Digest
			ComputeDigestFunc solver.ResultBasedCacheFunc
			PreprocessFunc    solver.PreprocessFunc
		}, v.depCount),
	}, true, nil
}

var _ solver.Op = &vertex{}

func (v *vertex) Digest() digest.Digest {
	return digest.FromBytes([]byte(v.opt.name))
}
func (v *vertex) Sys() any {
	return v
}
func (v *vertex) Inputs() []solver.Edge {
	return v.opt.inputs
}
func (v *vertex) Name() string {
	return v.opt.name
}
func (v *vertex) Options() solver.VertexOptions {
	var cache []solver.CacheManager
	if v.opt.cacheSource != nil {
		cache = append(cache, v.opt.cacheSource)
	}
	return solver.VertexOptions{
		CacheSources: cache,
		IgnoreCache:  v.opt.ignoreCache,
	}
}

func (v *vertex) setupCallCounters() {
	var cacheCount int64
	var execCount int64

	v.setCallCounters(&cacheCount, &execCount)
}

func (v *vertex) setCallCounters(cacheCount, execCount *int64) {
	v.cacheCallCount = cacheCount
	v.execCallCount = execCount

	for _, inp := range v.opt.inputs {
		var v *vertex
		switch vv := inp.Vertex.(type) {
		case *vertex:
			v = vv
		case *vertexSum:
			v = vv.vertex
		case *vertexAdd:
			v = vv.vertex
		case *vertexConst:
			v = vv.vertex
		case *vertexSubBuild:
			v = vv.vertex
		}
		v.setCallCounters(cacheCount, execCount)
	}
}

func (v *vertex) cacheMap(ctx context.Context) error {
	if f := v.opt.cachePreFunc; f != nil {
		if err := f(ctx); err != nil {
			return err
		}
	}
	if v.cacheCallCount != nil {
		atomic.AddInt64(v.cacheCallCount, 1)
	}
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	default:
	}
	select {
	case <-time.After(v.opt.cacheDelay):
	case <-ctx.Done():
		return context.Cause(ctx)
	}
	return nil
}

func (v *vertex) CacheMap(ctx context.Context, jobCtx solver.JobContext, index int) (*solver.CacheMap, bool, error) {
	if index == 0 {
		if err := v.cacheMap(ctx); err != nil {
			return nil, false, err
		}
		return v.makeCacheMap(), len(v.opt.cacheKeySeeds) == index, nil
	}
	return &solver.CacheMap{
		Digest: digest.FromBytes(fmt.Appendf(nil, "seed:%s", v.opt.cacheKeySeeds[index-1]())),
	}, len(v.opt.cacheKeySeeds) == index, nil
}

func (v *vertex) exec(ctx context.Context, inputs []solver.Result) error {
	if len(inputs) != len(v.Inputs()) {
		return errors.New("invalid number of inputs")
	}
	if f := v.opt.execPreFunc; f != nil {
		if err := f(ctx); err != nil {
			return err
		}
	}
	if v.execCallCount != nil {
		atomic.AddInt64(v.execCallCount, 1)
	}
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	default:
	}
	select {
	case <-time.After(v.opt.execDelay):
	case <-ctx.Done():
		return context.Cause(ctx)
	}
	return nil
}

func (v *vertex) Exec(ctx context.Context, job solver.JobContext, inputs []solver.Result) (outputs []solver.Result, err error) {
	if err := v.exec(ctx, inputs); err != nil {
		return nil, err
	}
	if v.opt.result != nil {
		return []solver.Result{v.opt.result}, nil
	}
	return []solver.Result{&dummyResult{id: identity.NewID(), value: v.opt.value}}, nil
}

func (v *vertex) Acquire(ctx context.Context) (solver.ReleaseFunc, error) {
	return func() {}, nil
}

func (v *vertex) makeCacheMap() *solver.CacheMap {
	m := &solver.CacheMap{
		Digest: digest.FromBytes(fmt.Appendf(nil, "seed:%s", v.opt.cacheKeySeed)),
		Deps: make([]struct {
			Selector          digest.Digest
			ComputeDigestFunc solver.ResultBasedCacheFunc
			PreprocessFunc    solver.PreprocessFunc
		}, len(v.Inputs())),
	}
	for i, f := range v.opt.slowCacheCompute {
		m.Deps[i].ComputeDigestFunc = f
	}
	for i, dgst := range v.opt.selectors {
		m.Deps[i].Selector = dgst
	}
	return m
}

// vtxConst returns a vertex that outputs a constant integer
func vtxConst(v int, opt vtxOpt) *vertexConst {
	if opt.cacheKeySeed == "" {
		opt.cacheKeySeed = fmt.Sprintf("const-%d", v)
	}
	if opt.name == "" {
		opt.name = opt.cacheKeySeed + "-" + identity.NewID()
	}
	return &vertexConst{vertex: vtx(opt), value: v}
}

type vertexConst struct {
	*vertex
	value int
}

var _ solver.Op = &vertexConst{}

func (v *vertexConst) Sys() any {
	return v
}

func (v *vertexConst) Exec(ctx context.Context, jobCtx solver.JobContext, inputs []solver.Result) (outputs []solver.Result, err error) {
	if err := v.exec(ctx, inputs); err != nil {
		return nil, err
	}
	return []solver.Result{&dummyResult{id: identity.NewID(), intValue: v.value}}, nil
}

func (v *vertexConst) Acquire(ctx context.Context) (solver.ReleaseFunc, error) {
	return func() {}, nil
}

// vtxSum returns a vertex that outputs sum of its inputs plus a constant
func vtxSum(v int, opt vtxOpt) *vertexSum {
	if opt.cacheKeySeed == "" {
		opt.cacheKeySeed = fmt.Sprintf("sum-%d-%d", v, len(opt.inputs))
	}
	if opt.name == "" {
		opt.name = opt.cacheKeySeed + "-" + identity.NewID()
	}
	return &vertexSum{vertex: vtx(opt), value: v}
}

type vertexSum struct {
	*vertex
	value int
}

var _ solver.Op = &vertexSum{}

func (v *vertexSum) Sys() any {
	return v
}

func (v *vertexSum) Exec(ctx context.Context, jobCtx solver.JobContext, inputs []solver.Result) (outputs []solver.Result, err error) {
	if err := v.exec(ctx, inputs); err != nil {
		return nil, err
	}
	s := v.value
	for _, inp := range inputs {
		r, ok := inp.Sys().(*dummyResult)
		if !ok {
			return nil, errors.Errorf("invalid input type: %T", inp.Sys())
		}
		s += r.intValue
	}
	return []solver.Result{&dummyResult{id: identity.NewID(), intValue: s}}, nil
}

func (v *vertexSum) Acquire(ctx context.Context) (solver.ReleaseFunc, error) {
	return func() {}, nil
}

// vtxAdd returns a vertex that outputs each input plus a constant
func vtxAdd(v int, opt vtxOpt) *vertexAdd {
	if opt.cacheKeySeed == "" {
		opt.cacheKeySeed = fmt.Sprintf("add-%d-%d", v, len(opt.inputs))
	}
	if opt.name == "" {
		opt.name = opt.cacheKeySeed + "-" + identity.NewID()
	}
	return &vertexAdd{vertex: vtx(opt), value: v}
}

type vertexAdd struct {
	*vertex
	value int
}

var _ solver.Op = &vertexAdd{}

func (v *vertexAdd) Sys() any {
	return v
}

func (v *vertexAdd) Exec(ctx context.Context, jobCtx solver.JobContext, inputs []solver.Result) (outputs []solver.Result, err error) {
	if err := v.exec(ctx, inputs); err != nil {
		return nil, err
	}
	for _, inp := range inputs {
		r, ok := inp.Sys().(*dummyResult)
		if !ok {
			return nil, errors.Errorf("invalid input type: %T", inp.Sys())
		}
		outputs = append(outputs, &dummyResult{id: identity.NewID(), intValue: r.intValue + v.value})
	}
	return outputs, nil
}

func (v *vertexAdd) Acquire(ctx context.Context) (solver.ReleaseFunc, error) {
	return func() {}, nil
}

func vtxSubBuild(g solver.Edge, opt vtxOpt) *vertexSubBuild {
	if opt.cacheKeySeed == "" {
		opt.cacheKeySeed = fmt.Sprintf("sub-%s", identity.NewID())
	}
	if opt.name == "" {
		opt.name = opt.cacheKeySeed + "-" + identity.NewID()
	}
	return &vertexSubBuild{vertex: vtx(opt), g: g}
}

type vertexSubBuild struct {
	*vertex
	g solver.Edge
	b solver.Builder
}

var _ solver.Op = &vertexSubBuild{}

func (v *vertexSubBuild) Sys() any {
	return v
}

func (v *vertexSubBuild) Exec(ctx context.Context, jobCtx solver.JobContext, inputs []solver.Result) (outputs []solver.Result, err error) {
	if err := v.exec(ctx, inputs); err != nil {
		return nil, err
	}
	res, err := v.b.Build(ctx, v.g)
	if err != nil {
		return nil, err
	}
	return []solver.Result{res}, nil
}

func (v *vertexSubBuild) Acquire(ctx context.Context) (solver.ReleaseFunc, error) {
	return func() {}, nil
}

type dummyResult struct {
	id       string
	value    string
	intValue int
}

func (r *dummyResult) ID() string                    { return r.id }
func (r *dummyResult) Release(context.Context) error { return nil }
func (r *dummyResult) Sys() any                      { return r }
func (r *dummyResult) Clone() solver.Result          { return r }

type countedResult struct {
	id           string
	value        string
	releaseCount *atomic.Int64
}

func (r *countedResult) ID() string { return r.id }
func (r *countedResult) Release(context.Context) error {
	if r.releaseCount != nil {
		r.releaseCount.Add(1)
	}
	return nil
}
func (r *countedResult) Sys() any { return &dummyResult{id: r.id, value: r.value} }
func (r *countedResult) Clone() solver.Result {
	return &countedResult{
		id:           r.id,
		value:        r.value,
		releaseCount: r.releaseCount,
	}
}

func testOpResolver(v solver.Vertex, b solver.Builder) (solver.Op, error) {
	if op, ok := v.Sys().(solver.Op); ok {
		if vtx, ok := op.(*vertexSubBuild); ok {
			vtx.b = b
		}
		return op, nil
	}

	return nil, errors.New("invalid vertex")
}

func unwrapInt(res solver.Result) int {
	r, ok := res.Sys().(*dummyResult)
	if !ok {
		return -1e6
	}
	return r.intValue
}

func blockingFunction(i int) func(context.Context) error {
	limit := int64(i)
	block := make(chan struct{})
	return func(context.Context) error {
		if atomic.AddInt64(&limit, -1) == 0 {
			close(block)
		}
		<-block
		return nil
	}
}

func unwrap(res solver.Result) string {
	r, ok := res.Sys().(*dummyResult)
	if !ok {
		return "unwrap-error"
	}
	return r.value
}

func newTrackingCacheManager(cm solver.CacheManager) *trackingCacheManager {
	return &trackingCacheManager{CacheManager: cm}
}

type trackingCacheManager struct {
	solver.CacheManager
	loadCounter int64
	forceFail   bool
}

func (cm *trackingCacheManager) Load(ctx context.Context, rec *solver.CacheRecord) (solver.Result, error) {
	atomic.AddInt64(&cm.loadCounter, 1)
	if cm.forceFail {
		return nil, errors.New("force fail")
	}
	return cm.CacheManager.Load(ctx, rec)
}

func digestFromResult(ctx context.Context, res solver.Result, _ session.Group) (digest.Digest, error) {
	return digest.FromBytes([]byte(unwrap(res))), nil
}

func testExporterOpts(all bool) solver.CacheExportOpt {
	mode := solver.CacheExportModeMin
	if all {
		mode = solver.CacheExportModeMax
	}
	return solver.CacheExportOpt{
		ResolveRemotes: func(ctx context.Context, res solver.Result) ([]*solver.Remote, error) {
			if dr, ok := res.Sys().(*dummyResult); ok {
				return []*solver.Remote{{Descriptors: []ocispecs.Descriptor{{
					Annotations: map[string]string{"value": fmt.Sprintf("%d", dr.intValue)},
				}}}}, nil
			}
			return nil, nil
		},
		Mode: mode,
	}
}

func newTestExporterTarget() *testExporterTarget {
	return &testExporterTarget{
		visited: map[any]struct{}{},
	}
}

type testExporterTarget struct {
	visited map[any]struct{}
	records []*testExporterRecord
}

func (t *testExporterTarget) numUniqueRecords() int {
	unique := make(map[digest.Digest]struct{})
	for _, r := range t.records {
		unique[r.dgst] = struct{}{}
	}
	return len(unique)
}

func (t *testExporterTarget) Add(dgst digest.Digest, links [][]solver.CacheLink, results []solver.CacheExportResult) (solver.CacheExporterRecord, bool, error) {
	r := &testExporterRecord{dgst: dgst, results: len(results), links: len(links)}
	t.records = append(t.records, r)
	return r, true, nil
}

type testExporterRecord struct {
	solver.CacheExporterRecordBase
	dgst    digest.Digest
	results int
	links   int
}

func dgst(s string) digest.Digest {
	return digest.FromBytes([]byte(s))
}
