package solver

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/session"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

func init() {
	if debugScheduler {
		logrus.SetOutput(os.Stdout)
		logrus.SetLevel(logrus.DebugLevel)
	}
}

func TestCacheErrNotFound(t *testing.T) {
	ctx := t.Context()

	cm := newTrackingCacheManager(NewInMemoryCacheManager())
	l := NewSolver(SolverOpt{
		ResolveOpFunc: testOpResolver,
		DefaultCache:  cm,
	})
	defer l.Close()

	j0, err := l.NewJob("j0")
	require.NoError(t, err)

	defer func() {
		if j0 != nil {
			j0.Discard()
		}
	}()

	g0 := Edge{
		Vertex: vtxSum(1, vtxOpt{
			inputs: []Edge{
				{Vertex: vtxConst(2, vtxOpt{})},
				{Vertex: vtxConst(3, vtxOpt{})},
			},
		}),
	}

	res, err := j0.Build(ctx, g0)
	require.NoError(t, err)
	require.Equal(t, 6, unwrapInt(res))

	require.NoError(t, j0.Discard())
	j0 = nil

	expTarget := newTestExporterTarget()

	key := res.CacheKeys()[0]
	internal := cm.CacheManager.(*cacheManager)
	store := internal.storage.(*kvCacheStorage).backend.(*inMemoryStore)
	exporter := key.Exporter.(*mergedExporter).exporters[0].(*exporter)
	record := exporter.record

	// Remove The first record from the internal store CacheManager to trigger an ErrNotFound
	delete(store.byID[internal.getID(record.key)].results, record.ID)
	_, err = exporter.ExportTo(ctx, expTarget, testExporterOpts(true))

	require.NoError(t, err)
}

type vtxOpt struct {
	name             string
	cacheKeySeed     string
	cacheKeySeeds    []func() string
	execDelay        time.Duration
	cacheDelay       time.Duration
	cachePreFunc     func(context.Context) error
	execPreFunc      func(context.Context) error
	inputs           []Edge
	value            string
	result           Result
	slowCacheCompute map[int]ResultBasedCacheFunc
	selectors        map[int]digest.Digest
	cacheSource      CacheManager
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

func (v *invalidCacheMapVertex) CacheMap(context.Context, JobContext, int) (*CacheMap, bool, error) {
	return &CacheMap{
		Digest: digest.FromBytes([]byte(v.Name())),
		Deps: make([]struct {
			Selector          digest.Digest
			ComputeDigestFunc ResultBasedCacheFunc
			PreprocessFunc    PreprocessFunc
		}, v.depCount),
	}, true, nil
}

var _ Op = &vertex{}

func (v *vertex) Digest() digest.Digest {
	return digest.FromBytes([]byte(v.opt.name))
}
func (v *vertex) Sys() any {
	return v
}
func (v *vertex) Inputs() []Edge {
	return v.opt.inputs
}
func (v *vertex) Name() string {
	return v.opt.name
}
func (v *vertex) Options() VertexOptions {
	var cache []CacheManager
	if v.opt.cacheSource != nil {
		cache = append(cache, v.opt.cacheSource)
	}
	return VertexOptions{
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

func (v *vertex) CacheMap(ctx context.Context, jobCtx JobContext, index int) (*CacheMap, bool, error) {
	if index == 0 {
		if err := v.cacheMap(ctx); err != nil {
			return nil, false, err
		}
		return v.makeCacheMap(), len(v.opt.cacheKeySeeds) == index, nil
	}
	return &CacheMap{
		Digest: digest.FromBytes(fmt.Appendf(nil, "seed:%s", v.opt.cacheKeySeeds[index-1]())),
	}, len(v.opt.cacheKeySeeds) == index, nil
}

func (v *vertex) exec(ctx context.Context, inputs []Result) error {
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

func (v *vertex) Exec(ctx context.Context, job JobContext, inputs []Result) (outputs []Result, err error) {
	if err := v.exec(ctx, inputs); err != nil {
		return nil, err
	}
	if v.opt.result != nil {
		return []Result{v.opt.result}, nil
	}
	return []Result{&dummyResult{id: identity.NewID(), value: v.opt.value}}, nil
}

func (v *vertex) Acquire(ctx context.Context) (ReleaseFunc, error) {
	return func() {}, nil
}

func (v *vertex) makeCacheMap() *CacheMap {
	m := &CacheMap{
		Digest: digest.FromBytes(fmt.Appendf(nil, "seed:%s", v.opt.cacheKeySeed)),
		Deps: make([]struct {
			Selector          digest.Digest
			ComputeDigestFunc ResultBasedCacheFunc
			PreprocessFunc    PreprocessFunc
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

var _ Op = &vertexConst{}

func (v *vertexConst) Sys() any {
	return v
}

func (v *vertexConst) Exec(ctx context.Context, jobCtx JobContext, inputs []Result) (outputs []Result, err error) {
	if err := v.exec(ctx, inputs); err != nil {
		return nil, err
	}
	return []Result{&dummyResult{id: identity.NewID(), intValue: v.value}}, nil
}

func (v *vertexConst) Acquire(ctx context.Context) (ReleaseFunc, error) {
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

var _ Op = &vertexSum{}

func (v *vertexSum) Sys() any {
	return v
}

func (v *vertexSum) Exec(ctx context.Context, jobCtx JobContext, inputs []Result) (outputs []Result, err error) {
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
	return []Result{&dummyResult{id: identity.NewID(), intValue: s}}, nil
}

func (v *vertexSum) Acquire(ctx context.Context) (ReleaseFunc, error) {
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

var _ Op = &vertexAdd{}

func (v *vertexAdd) Sys() any {
	return v
}

func (v *vertexAdd) Exec(ctx context.Context, jobCtx JobContext, inputs []Result) (outputs []Result, err error) {
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

func (v *vertexAdd) Acquire(ctx context.Context) (ReleaseFunc, error) {
	return func() {}, nil
}

func PrintGraph(e Edge, pfx string) {
	name := e.Vertex.Name()
	fmt.Printf("%s %d %s\n", pfx, e.Index, name)
	for _, inp := range e.Vertex.Inputs() {
		PrintGraph(inp, pfx+"-->")
	}
}

type dummyResult struct {
	id       string
	value    string
	intValue int
}

func (r *dummyResult) ID() string                    { return r.id }
func (r *dummyResult) Release(context.Context) error { return nil }
func (r *dummyResult) Sys() any                      { return r }
func (r *dummyResult) Clone() Result                 { return r }

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
func (r *countedResult) Clone() Result {
	return &countedResult{
		id:           r.id,
		value:        r.value,
		releaseCount: r.releaseCount,
	}
}

func testOpResolver(v Vertex, b Builder) (Op, error) {
	if op, ok := v.Sys().(Op); ok {
		return op, nil
	}
	return nil, errors.New("invalid vertex")
}

func unwrap(res Result) string {
	r, ok := res.Sys().(*dummyResult)
	if !ok {
		return "unwrap-error"
	}
	return r.value
}

func unwrapInt(res Result) int {
	r, ok := res.Sys().(*dummyResult)
	if !ok {
		return -1e6
	}
	return r.intValue
}

func blockingFuncion(i int) func(context.Context) error {
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

func newTrackingCacheManager(cm CacheManager) *trackingCacheManager {
	return &trackingCacheManager{CacheManager: cm}
}

type trackingCacheManager struct {
	CacheManager
	loadCounter int64
	forceFail   bool
}

func (cm *trackingCacheManager) Load(ctx context.Context, rec *CacheRecord) (Result, error) {
	atomic.AddInt64(&cm.loadCounter, 1)
	if cm.forceFail {
		return nil, errors.New("force fail")
	}
	return cm.CacheManager.Load(ctx, rec)
}

func digestFromResult(ctx context.Context, res Result, _ session.Group) (digest.Digest, error) {
	return digest.FromBytes([]byte(unwrap(res))), nil
}

func testExporterOpts(all bool) CacheExportOpt {
	mode := CacheExportModeMin
	if all {
		mode = CacheExportModeMax
	}
	return CacheExportOpt{
		ResolveRemotes: func(ctx context.Context, res Result) ([]*Remote, error) {
			if dr, ok := res.Sys().(*dummyResult); ok {
				return []*Remote{{Descriptors: []ocispecs.Descriptor{{
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

func (t *testExporterTarget) Add(dgst digest.Digest, links [][]CacheLink, results []CacheExportResult) (CacheExporterRecord, bool, error) {
	r := &testExporterRecord{dgst: dgst, results: len(results), links: len(links)}
	t.records = append(t.records, r)
	return r, true, nil
}

type testExporterRecord struct {
	CacheExporterRecordBase
	dgst    digest.Digest
	results int
	links   int
}
