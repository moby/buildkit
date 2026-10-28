package solver

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/moby/buildkit/identity"
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
	store := internal.backend.(*inMemoryStore)
	exporter := key.Exporter.(*mergedExporter).exporters[0].(*exporter)
	record := exporter.record

	// Remove The first record from the internal store CacheManager to trigger an ErrNotFound
	delete(store.byID[internal.getID(record.key)].results, record.ID)
	_, err = exporter.ExportTo(ctx, expTarget, testExporterOpts(true))

	require.NoError(t, err)
}

type vtxOpt struct {
	name          string
	cacheKeySeed  string
	cacheKeySeeds []func() string
	inputs        []Edge
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

func (v *vertex) makeCacheMap() *CacheMap {
	m := &CacheMap{
		Digest: digest.FromBytes(fmt.Appendf(nil, "seed:%s", v.opt.cacheKeySeed)),
		Deps: make([]struct {
			Selector          digest.Digest
			ComputeDigestFunc ResultBasedCacheFunc
			PreprocessFunc    PreprocessFunc
		}, len(v.Inputs())),
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
