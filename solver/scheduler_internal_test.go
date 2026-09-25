package solver

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/util/progress"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

func init() {
	if debugScheduler {
		logrus.SetOutput(os.Stdout)
		logrus.SetLevel(logrus.DebugLevel)
	}
}

func TestProgressSnapshotDuringCompletion(t *testing.T) {
	t.Parallel()
	for range 100 {
		pr, ctx, cancel := progress.NewContext(t.Context())
		pw, _, _ := progress.NewFromContext(ctx)
		j := &Job{pw: pw}
		st := &state{
			mpw:   progress.NewMultiWriter(),
			allPw: map[progress.Writer]struct{}{},
			jobs:  map[*Job]struct{}{j: {}},
		}
		completed := st.notifyStarted(progress.WithProgress(ctx, st.mpw), false)
		start := make(chan struct{})
		var eg errgroup.Group
		eg.Go(func() error {
			<-start
			completed(nil, false)
			return nil
		})
		eg.Go(func() error {
			<-start
			(&Solver{}).connectProgressFromState(st, st)
			return nil
		})
		close(start)
		err := eg.Wait()
		pw.Close()
		cancel(nil)
		require.NoError(t, err)
		items, err := pr.Read(t.Context())
		require.NoError(t, err)
		require.NotEmpty(t, items)
		last := items[len(items)-1].Sys.(client.Vertex)
		require.NotNil(t, last.Completed, "snapshot must not overwrite completion")
	}
}

func TestCacheOptsDuringProgress(t *testing.T) {
	t.Parallel()
	st := &state{
		op: &sharedOp{
			cacheRes: []*CacheMap{{Opts: CacheOpts{"test": "value"}}},
		},
	}
	ctx := withAncestorCacheOpts(t.Context(), st)
	getter := CacheOptGetterOf(ctx)

	completed := st.notifyStarted(ctx, false)
	require.Equal(t, "value", getter(false, "test")["test"])
	completed(errors.New("operation failed"), false)
	require.Empty(t, getter(false, "test"))

	completed = st.notifyStarted(ctx, false)
	completed(nil, false)
	require.Equal(t, "value", getter(false, "test")["test"])

	// Cache option lookups can run while another operation reports progress.
	var eg errgroup.Group
	eg.Go(func() error {
		for range 100 {
			completed := st.notifyStarted(ctx, false)
			completed(errors.New("operation failed"), false)
			completed = st.notifyStarted(ctx, true)
			completed(nil, true)
		}
		return nil
	})
	eg.Go(func() error {
		for range 100 {
			if value, ok := getter(false, "test")["test"]; ok {
				require.Equal(t, "value", value)
			}
		}
		return nil
	})
	require.NoError(t, eg.Wait())
}

func TestCacheErrNotFound(t *testing.T) {
	ctx := t.Context()

	cm := NewInMemoryCacheManager()
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
	internal := cm.(*cacheManager)
	store := internal.storage.(*kvCacheStorage).backend.(*inMemoryStore)
	exporter := key.Exporter.(*mergedExporter).exporters[0].(*exporter)
	record := exporter.record

	// Remove The first record from the internal store CacheManager to trigger an ErrNotFound
	delete(store.byID[internal.getID(record.key)].results, record.ID)
	_, err = exporter.ExportTo(ctx, expTarget, testExporterOpts(true))

	require.NoError(t, err)
}

// TestCacheExportDuringEdgeMerge exports a cache-loaded result while another
// job merges into its edge. Run with -race.
func TestCacheExportDuringEdgeMerge(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	l := NewSolver(SolverOpt{
		ResolveOpFunc: testOpResolver,
		DefaultCache:  NewInMemoryCacheManager(),
	})
	defer l.Close()

	// vertexes with the same cache key seed and input results share a slow
	// cache key, even with different inputs
	g := func(name, input string) Edge {
		return Edge{
			Vertex: vtx(vtxOpt{
				name:         name,
				cacheKeySeed: "seed1",
				value:        "result1",
				inputs: []Edge{{Vertex: vtx(vtxOpt{
					name:         input,
					cacheKeySeed: "seed-" + input,
					value:        "result2",
				})}},
				slowCacheCompute: map[int]ResultBasedCacheFunc{0: digestFromResult},
			}),
		}
	}

	j0, err := l.NewJob("j0")
	require.NoError(t, err)
	defer func() {
		if j0 != nil {
			j0.Discard()
		}
	}()
	_, err = j0.Build(ctx, g("v1", "v2"))
	require.NoError(t, err)
	require.NoError(t, j0.Discard())
	j0 = nil

	// j1 loads the result through the slow cache key, so its exporter
	// references the active edge
	j1, err := l.NewJob("j1")
	require.NoError(t, err)
	defer j1.Discard()
	res, err := j1.Build(ctx, g("v1", "v4"))
	require.NoError(t, err)

	started := make(chan struct{})
	done := make(chan struct{})
	eg, egCtx := errgroup.WithContext(ctx)
	eg.Go(func() error {
		for i := 0; ; i++ {
			if i == 1 {
				close(started)
			}
			select {
			case <-done:
				return nil
			default:
			}
			if _, err := res.CacheKeys()[0].Exporter.ExportTo(egCtx, newTestExporterTarget(), testExporterOpts(true)); err != nil {
				return err
			}
		}
	})
	// keep exporting while j2 merges into the j1 edge
	select {
	case <-started:
	case <-egCtx.Done():
	}

	j2, err := l.NewJob("j2")
	require.NoError(t, err)
	defer j2.Discard()
	_, err = j2.Build(ctx, g("v1-diff", "v3"))
	close(done)
	require.NoError(t, err)
	require.NoError(t, eg.Wait())

	// the j2 edge was merged into the j1 edge
	e := res.CacheKeys()[0].Exporter.(*exporter).edge
	e.secondaryExportersMu.Lock()
	defer e.secondaryExportersMu.Unlock()
	require.NotEmpty(t, e.secondaryExporters)
}

type vtxOpt struct {
	name             string
	cacheKeySeed     string
	cacheKeySeeds    []func() string
	inputs           []Edge
	value            string
	slowCacheCompute map[int]ResultBasedCacheFunc
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
}

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
	return VertexOptions{}
}

func (v *vertex) CacheMap(ctx context.Context, jobCtx JobContext, index int) (*CacheMap, bool, error) {
	if index == 0 {
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
	return context.Cause(ctx)
}

func (v *vertex) Exec(ctx context.Context, job JobContext, inputs []Result) (outputs []Result, err error) {
	if err := v.exec(ctx, inputs); err != nil {
		return nil, err
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

type dummyResult struct {
	id       string
	value    string
	intValue int
}

func (r *dummyResult) ID() string                    { return r.id }
func (r *dummyResult) Release(context.Context) error { return nil }
func (r *dummyResult) Sys() any                      { return r }
func (r *dummyResult) Clone() Result                 { return r }

func testOpResolver(v Vertex, b Builder) (Op, error) {
	if op, ok := v.Sys().(Op); ok {
		return op, nil
	}
	return nil, errors.New("invalid vertex")
}

func unwrapInt(res Result) int {
	r, ok := res.Sys().(*dummyResult)
	if !ok {
		return -1e6
	}
	return r.intValue
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
	return &testExporterTarget{}
}

type testExporterTarget struct{}

func (t *testExporterTarget) Add(dgst digest.Digest, links [][]CacheLink, results []CacheExportResult) (CacheExporterRecord, bool, error) {
	return &testExporterRecord{}, true, nil
}

type testExporterRecord struct {
	CacheExporterRecordBase
}
