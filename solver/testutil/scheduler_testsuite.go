package testutil

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/solver"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

type (
	NewCacheManagerFunc func(t *testing.T) solver.CacheManager
	SolverTestOptions   struct {
		NewCacheManager NewCacheManagerFunc
	}
)

var schedulerTestSuite = []func(t *testing.T, opts SolverTestOptions){
	testSingleLevelActiveGraph,
	testSingleLevelCache,
	testSingleLevelCacheParallel,
	testMultiLevelCacheParallel,
	testSingleCancelCache,
	testSingleCancelExec,
	testSingleCancelParallel,
	testMultiLevelCalculation,
	testHugeGraph,
	testOptimizedCacheAccess,
	testOptimizedCacheAccess2,
	testSlowCache,
	testSlowCacheErrorResultReleasedWithJob,
	testParallelInputs,
	testErrorReturns,
	testMultipleCacheSources,
	testRepeatBuildWithIgnoreCache,
	testIgnoreCacheResumeFromSlowCache,
	testParallelBuildsIgnoreCache,
	testSubbuild,
	testCacheWithSelector,
	testCacheSlowWithSelector,
	testCacheExporting,
	testCacheExportingModeMin,
	testSlowCacheAvoidAccess,
	testSlowCacheAvoidLoadOnCache,
	testCacheMultipleMaps,
	testCacheInputMultipleMaps,
	testCacheExportingPartialSelector,
	testCacheExportingMergedKey,
	testMergedEdgesLookup,
	testMergedEdgesCycle,
	testMergedEdgesCycleMultipleOwners,
	testCacheErrNotFound,
	testCacheLoadError,
	testInputRequestDeadlock,
	testCacheMapDependencyCount,
	testStaleEdgeMerge,
	testUnknownBuildID,
	testCacheMissStaleCompleteSharedDep,
}

func RunSolverTests(t *testing.T, opts SolverTestOptions) {
	if opts.NewCacheManager == nil {
		opts.NewCacheManager = func(t *testing.T) solver.CacheManager {
			return solver.NewInMemoryCacheManager()
		}
	}

	for _, fn := range schedulerTestSuite {
		t.Run(getFunctionName(fn), func(t *testing.T) {
			fn(t, opts)
		})
	}
}

func testSingleLevelActiveGraph(t *testing.T, opts SolverTestOptions) {
	t.Parallel()
	ctx := t.Context()

	s := solver.NewSolver(solver.SolverOpt{
		ResolveOpFunc: testOpResolver,
		DefaultCache:  opts.NewCacheManager(t),
	})
	defer s.Close()

	j0, err := s.NewJob("job0")
	require.NoError(t, err)

	defer func() {
		if j0 != nil {
			j0.Discard()
		}
	}()

	g0 := solver.Edge{
		Vertex: vtx(vtxOpt{
			name:  "v0",
			value: "result0",
		}),
	}
	g0.Vertex.(*vertex).setupCallCounters()

	res, err := j0.Build(ctx, g0)
	require.NoError(t, err)
	require.NotNil(t, res)
	require.Equal(t, "result0", unwrap(res))

	require.Equal(t, int64(1), *g0.Vertex.(*vertex).cacheCallCount)
	require.Equal(t, int64(1), *g0.Vertex.(*vertex).execCallCount)

	// calling again with same digest just uses the active queue
	j1, err := s.NewJob("job1")
	require.NoError(t, err)

	defer func() {
		if j1 != nil {
			j1.Discard()
		}
	}()

	g1 := solver.Edge{
		Vertex: vtx(vtxOpt{
			name:  "v0",
			value: "result1",
		}),
	}
	g1.Vertex.(*vertex).setupCallCounters()

	res, err = j1.Build(ctx, g1)
	require.NoError(t, err)
	require.Equal(t, "result0", unwrap(res))

	require.Equal(t, int64(1), *g0.Vertex.(*vertex).cacheCallCount)
	require.Equal(t, int64(1), *g0.Vertex.(*vertex).execCallCount)
	require.Equal(t, int64(0), *g1.Vertex.(*vertex).cacheCallCount)
	require.Equal(t, int64(0), *g1.Vertex.(*vertex).execCallCount)

	require.NoError(t, j0.Discard())
	j0 = nil

	// after discarding j0, j1 still holds the state

	j2, err := s.NewJob("job2")
	require.NoError(t, err)

	defer func() {
		if j2 != nil {
			j2.Discard()
		}
	}()

	g2 := solver.Edge{
		Vertex: vtx(vtxOpt{
			name:  "v0",
			value: "result2",
		}),
	}
	g2.Vertex.(*vertex).setupCallCounters()

	res, err = j2.Build(ctx, g2)
	require.NoError(t, err)
	require.Equal(t, "result0", unwrap(res))

	require.Equal(t, int64(1), *g0.Vertex.(*vertex).cacheCallCount)
	require.Equal(t, int64(1), *g0.Vertex.(*vertex).execCallCount)
	require.Equal(t, int64(0), *g1.Vertex.(*vertex).cacheCallCount)
	require.Equal(t, int64(0), *g1.Vertex.(*vertex).execCallCount)
	require.Equal(t, int64(0), *g2.Vertex.(*vertex).cacheCallCount)
	require.Equal(t, int64(0), *g2.Vertex.(*vertex).execCallCount)

	require.NoError(t, j1.Discard())
	j1 = nil
	require.NoError(t, j2.Discard())
	j2 = nil

	// everything should be released now

	j3, err := s.NewJob("job3")
	require.NoError(t, err)

	defer func() {
		if j3 != nil {
			j3.Discard()
		}
	}()

	g3 := solver.Edge{
		Vertex: vtx(vtxOpt{
			name:  "v0",
			value: "result3",
		}),
	}
	g3.Vertex.(*vertex).setupCallCounters()

	res, err = j3.Build(ctx, g3)
	require.NoError(t, err)
	require.Equal(t, "result3", unwrap(res))

	require.Equal(t, int64(1), *g3.Vertex.(*vertex).cacheCallCount)
	require.Equal(t, int64(1), *g3.Vertex.(*vertex).execCallCount)

	require.NoError(t, j3.Discard())
	j3 = nil

	// repeat the same test but make sure the build run in parallel now

	j4, err := s.NewJob("job4")
	require.NoError(t, err)

	defer func() {
		if j4 != nil {
			j4.Discard()
		}
	}()

	j5, err := s.NewJob("job5")
	require.NoError(t, err)

	defer func() {
		if j5 != nil {
			j5.Discard()
		}
	}()

	g4 := solver.Edge{
		Vertex: vtx(vtxOpt{
			name:       "v0",
			cacheDelay: 100 * time.Millisecond,
			value:      "result4",
		}),
	}
	g4.Vertex.(*vertex).setupCallCounters()

	eg, _ := errgroup.WithContext(ctx)

	eg.Go(func() error {
		res, err := j4.Build(ctx, g4)
		require.NoError(t, err)
		require.Equal(t, "result4", unwrap(res))
		return err
	})

	eg.Go(func() error {
		res, err := j5.Build(ctx, g4)
		require.NoError(t, err)
		require.Equal(t, "result4", unwrap(res))
		return err
	})

	require.NoError(t, eg.Wait())

	require.Equal(t, int64(1), *g4.Vertex.(*vertex).cacheCallCount)
	require.Equal(t, int64(1), *g4.Vertex.(*vertex).execCallCount)
}

func testSingleLevelCache(t *testing.T, opts SolverTestOptions) {
	t.Parallel()
	ctx := t.Context()

	s := solver.NewSolver(solver.SolverOpt{
		ResolveOpFunc: testOpResolver,
		DefaultCache:  opts.NewCacheManager(t),
	})
	defer s.Close()

	j0, err := s.NewJob("job0")
	require.NoError(t, err)

	defer func() {
		if j0 != nil {
			j0.Discard()
		}
	}()

	g0 := solver.Edge{
		Vertex: vtx(vtxOpt{
			name:         "v0",
			cacheKeySeed: "seed0",
			value:        "result0",
		}),
	}
	g0.Vertex.(*vertex).setupCallCounters()

	res, err := j0.Build(ctx, g0)
	require.NoError(t, err)
	require.Equal(t, "result0", unwrap(res))

	require.NoError(t, j0.Discard())
	j0 = nil

	// first try that there is no match for different cache
	j1, err := s.NewJob("job1")
	require.NoError(t, err)

	defer func() {
		if j1 != nil {
			j1.Discard()
		}
	}()

	g1 := solver.Edge{
		Vertex: vtx(vtxOpt{
			name:         "v1",
			cacheKeySeed: "seed1",
			value:        "result1",
		}),
	}
	g1.Vertex.(*vertex).setupCallCounters()

	res, err = j1.Build(ctx, g1)
	require.NoError(t, err)
	require.Equal(t, "result1", unwrap(res))

	require.Equal(t, int64(1), *g1.Vertex.(*vertex).cacheCallCount)
	require.Equal(t, int64(1), *g1.Vertex.(*vertex).execCallCount)

	require.NoError(t, j1.Discard())
	j1 = nil

	// expect cache match for first build

	j2, err := s.NewJob("job2")
	require.NoError(t, err)

	defer func() {
		if j2 != nil {
			j2.Discard()
		}
	}()

	g2 := solver.Edge{
		Vertex: vtx(vtxOpt{
			name:         "v2",
			cacheKeySeed: "seed0", // same as first build
			value:        "result2",
		}),
	}
	g2.Vertex.(*vertex).setupCallCounters()

	res, err = j2.Build(ctx, g2)
	require.NoError(t, err)
	require.Equal(t, "result0", unwrap(res))

	require.Equal(t, int64(1), *g0.Vertex.(*vertex).cacheCallCount)
	require.Equal(t, int64(1), *g0.Vertex.(*vertex).execCallCount)
	require.Equal(t, int64(1), *g2.Vertex.(*vertex).cacheCallCount)
	require.Equal(t, int64(0), *g2.Vertex.(*vertex).execCallCount)

	require.NoError(t, j2.Discard())
	j2 = nil
}

func testSingleLevelCacheParallel(t *testing.T, opts SolverTestOptions) {
	t.Parallel()
	ctx := t.Context()

	s := solver.NewSolver(solver.SolverOpt{
		ResolveOpFunc: testOpResolver,
		DefaultCache:  opts.NewCacheManager(t),
	})
	defer s.Close()

	// rebuild in parallel. only executed once.

	j0, err := s.NewJob("job0")
	require.NoError(t, err)

	defer func() {
		if j0 != nil {
			j0.Discard()
		}
	}()

	wait2Ready := blockingFunction(2)

	g0 := solver.Edge{
		Vertex: vtx(vtxOpt{
			name:         "v0",
			cacheKeySeed: "seed0",
			cachePreFunc: wait2Ready,
			value:        "result0",
		}),
	}
	g0.Vertex.(*vertex).setupCallCounters()

	j1, err := s.NewJob("job1")
	require.NoError(t, err)

	defer func() {
		if j1 != nil {
			j1.Discard()
		}
	}()

	g1 := solver.Edge{
		Vertex: vtx(vtxOpt{
			name:         "v1",
			cacheKeySeed: "seed0", // same as g0
			cachePreFunc: wait2Ready,
			value:        "result0",
		}),
	}
	g1.Vertex.(*vertex).setupCallCounters()

	eg, _ := errgroup.WithContext(ctx)

	eg.Go(func() error {
		res, err := j0.Build(ctx, g0)
		require.NoError(t, err)
		require.Equal(t, "result0", unwrap(res))
		return err
	})

	eg.Go(func() error {
		res, err := j1.Build(ctx, g1)
		require.NoError(t, err)
		require.Equal(t, "result0", unwrap(res))
		return err
	})

	require.NoError(t, eg.Wait())

	require.Equal(t, int64(1), *g0.Vertex.(*vertex).cacheCallCount)
	require.Equal(t, int64(1), *g1.Vertex.(*vertex).cacheCallCount)
	// only one execution ran
	require.Equal(t, int64(1), *g0.Vertex.(*vertex).execCallCount+*g1.Vertex.(*vertex).execCallCount)
}

func testMultiLevelCacheParallel(t *testing.T, opts SolverTestOptions) {
	t.Parallel()
	ctx := t.Context()

	s := solver.NewSolver(solver.SolverOpt{
		ResolveOpFunc: testOpResolver,
		DefaultCache:  opts.NewCacheManager(t),
	})
	defer s.Close()

	// rebuild in parallel. only executed once.

	j0, err := s.NewJob("job0")
	require.NoError(t, err)

	defer func() {
		if j0 != nil {
			j0.Discard()
		}
	}()

	wait2Ready := blockingFunction(2)
	wait2Ready2 := blockingFunction(2)

	g0 := solver.Edge{
		Vertex: vtx(vtxOpt{
			name:         "v0",
			cacheKeySeed: "seed0",
			cachePreFunc: wait2Ready,
			value:        "result0",
			inputs: []solver.Edge{{
				Vertex: vtx(vtxOpt{
					name:         "v0-c0",
					cacheKeySeed: "seed0-c0",
					cachePreFunc: wait2Ready2,
					value:        "result0-c0",
				})},
			},
		}),
	}
	g0.Vertex.(*vertex).setupCallCounters()

	j1, err := s.NewJob("job1")
	require.NoError(t, err)

	defer func() {
		if j1 != nil {
			j1.Discard()
		}
	}()

	g1 := solver.Edge{
		Vertex: vtx(vtxOpt{
			name:         "v1",
			cacheKeySeed: "seed0", // same as g0
			cachePreFunc: wait2Ready,
			value:        "result0",
			inputs: []solver.Edge{{
				Vertex: vtx(vtxOpt{
					name:         "v1-c0",
					cacheKeySeed: "seed0-c0", // same as g0
					cachePreFunc: wait2Ready2,
					value:        "result0-c",
				})},
			},
		}),
	}
	g1.Vertex.(*vertex).setupCallCounters()

	eg, _ := errgroup.WithContext(ctx)

	eg.Go(func() error {
		res, err := j0.Build(ctx, g0)
		require.NoError(t, err)
		require.Equal(t, "result0", unwrap(res))
		return err
	})

	eg.Go(func() error {
		res, err := j1.Build(ctx, g1)
		require.NoError(t, err)
		require.Equal(t, "result0", unwrap(res))
		return err
	})

	require.NoError(t, eg.Wait())

	require.Equal(t, int64(2), *g0.Vertex.(*vertex).cacheCallCount)
	require.Equal(t, int64(2), *g1.Vertex.(*vertex).cacheCallCount)
	require.Equal(t, int64(2), *g0.Vertex.(*vertex).execCallCount+*g1.Vertex.(*vertex).execCallCount)
}

func testSingleCancelCache(t *testing.T, opts SolverTestOptions) {
	t.Parallel()
	ctx := t.Context()

	s := solver.NewSolver(solver.SolverOpt{
		ResolveOpFunc: testOpResolver,
		DefaultCache:  opts.NewCacheManager(t),
	})
	defer s.Close()

	j0, err := s.NewJob("job0")
	require.NoError(t, err)

	defer func() {
		if j0 != nil {
			j0.Discard()
		}
	}()

	ctx, cancel := context.WithCancelCause(ctx)

	g0 := solver.Edge{
		Vertex: vtx(vtxOpt{
			name: "v0",
			cachePreFunc: func(ctx context.Context) error {
				cancel(errors.WithStack(context.Canceled))
				<-ctx.Done()
				return nil // error should still come from context
			},
		}),
	}
	g0.Vertex.(*vertex).setupCallCounters()

	_, err = j0.Build(ctx, g0)
	require.Error(t, err)
	require.Equal(t, true, errors.Is(err, context.Canceled))

	require.Equal(t, int64(1), *g0.Vertex.(*vertex).cacheCallCount)
	require.Equal(t, int64(0), *g0.Vertex.(*vertex).execCallCount)

	require.NoError(t, j0.Discard())
	j0 = nil
}

func testSingleCancelExec(t *testing.T, opts SolverTestOptions) {
	t.Parallel()
	ctx := t.Context()

	s := solver.NewSolver(solver.SolverOpt{
		ResolveOpFunc: testOpResolver,
		DefaultCache:  opts.NewCacheManager(t),
	})
	defer s.Close()

	j1, err := s.NewJob("job1")
	require.NoError(t, err)

	defer func() {
		if j1 != nil {
			j1.Discard()
		}
	}()

	ctx, cancel := context.WithCancelCause(ctx)

	g1 := solver.Edge{
		Vertex: vtx(vtxOpt{
			name: "v2",
			execPreFunc: func(ctx context.Context) error {
				cancel(errors.WithStack(context.Canceled))
				<-ctx.Done()
				return nil // error should still come from context
			},
		}),
	}
	g1.Vertex.(*vertex).setupCallCounters()

	_, err = j1.Build(ctx, g1)
	require.Error(t, err)
	require.Equal(t, true, errors.Is(err, context.Canceled))

	require.Equal(t, int64(1), *g1.Vertex.(*vertex).cacheCallCount)
	require.Equal(t, int64(1), *g1.Vertex.(*vertex).execCallCount)

	require.NoError(t, j1.Discard())
	j1 = nil
}

func testSingleCancelParallel(t *testing.T, opts SolverTestOptions) {
	t.Parallel()
	ctx := t.Context()

	s := solver.NewSolver(solver.SolverOpt{
		ResolveOpFunc: testOpResolver,
		DefaultCache:  opts.NewCacheManager(t),
	})
	defer s.Close()

	// run 2 in parallel cancel first, second one continues without errors
	eg, ctx := errgroup.WithContext(ctx)

	firstReady := make(chan struct{})
	firstErrored := make(chan struct{})

	eg.Go(func() error {
		j, err := s.NewJob("job2")
		require.NoError(t, err)

		defer func() {
			if j != nil {
				j.Discard()
			}
		}()

		ctx, cancel := context.WithCancelCause(ctx)
		defer func() { cancel(errors.WithStack(context.Canceled)) }()

		g := solver.Edge{
			Vertex: vtx(vtxOpt{
				name:  "v2",
				value: "result2",
				cachePreFunc: func(ctx context.Context) error {
					close(firstReady)
					time.Sleep(200 * time.Millisecond)
					cancel(errors.WithStack(context.Canceled))
					<-firstErrored
					return nil
				},
			}),
		}

		_, err = j.Build(ctx, g)
		close(firstErrored)
		require.Error(t, err)
		require.Equal(t, true, errors.Is(err, context.Canceled))
		return nil
	})

	eg.Go(func() error {
		j, err := s.NewJob("job3")
		require.NoError(t, err)

		defer func() {
			if j != nil {
				j.Discard()
			}
		}()

		g := solver.Edge{
			Vertex: vtx(vtxOpt{
				name: "v2",
			}),
		}
		<-firstReady

		res, err := j.Build(ctx, g)
		require.NoError(t, err)
		require.Equal(t, "result2", unwrap(res))
		return err
	})

	require.NoError(t, eg.Wait())
}

func testMultiLevelCalculation(t *testing.T, opts SolverTestOptions) {
	t.Parallel()
	ctx := t.Context()

	l := solver.NewSolver(solver.SolverOpt{
		ResolveOpFunc: testOpResolver,
		DefaultCache:  opts.NewCacheManager(t),
	})
	defer l.Close()

	j0, err := l.NewJob("j0")
	require.NoError(t, err)

	defer func() {
		if j0 != nil {
			j0.Discard()
		}
	}()

	g := solver.Edge{
		Vertex: vtxSum(1, vtxOpt{
			inputs: []solver.Edge{
				{Vertex: vtxSum(0, vtxOpt{
					inputs: []solver.Edge{
						{Vertex: vtxConst(7, vtxOpt{})},
						{Vertex: vtxConst(2, vtxOpt{})},
					},
				})},
				{Vertex: vtxSum(0, vtxOpt{
					inputs: []solver.Edge{
						{Vertex: vtxConst(7, vtxOpt{})},
						{Vertex: vtxConst(2, vtxOpt{})},
					},
				})},
				{Vertex: vtxConst(2, vtxOpt{})},
				{Vertex: vtxConst(2, vtxOpt{})},
				{Vertex: vtxConst(19, vtxOpt{})},
			},
		}),
	}

	res, err := j0.Build(ctx, g)
	require.NoError(t, err)
	require.Equal(t, 42, unwrapInt(res)) // 1 + 2*(7 + 2) + 2 + 2 + 19

	require.NoError(t, j0.Discard())
	j0 = nil

	// repeating same build with cache should behave the same
	j1, err := l.NewJob("j1")
	require.NoError(t, err)

	defer func() {
		if j1 != nil {
			j1.Discard()
		}
	}()

	g2 := solver.Edge{
		Vertex: vtxSum(1, vtxOpt{
			inputs: []solver.Edge{
				{Vertex: vtxSum(0, vtxOpt{
					inputs: []solver.Edge{
						{Vertex: vtxConst(7, vtxOpt{})},
						{Vertex: vtxConst(2, vtxOpt{})},
					},
				})},
				{Vertex: vtxSum(0, vtxOpt{
					inputs: []solver.Edge{
						{Vertex: vtxConst(7, vtxOpt{})},
						{Vertex: vtxConst(2, vtxOpt{})},
					},
				})},
				{Vertex: vtxConst(2, vtxOpt{})},
				{Vertex: vtxConst(2, vtxOpt{})},
				{Vertex: vtxConst(19, vtxOpt{})},
			},
		}),
	}
	res, err = j1.Build(ctx, g2)
	require.NoError(t, err)
	require.Equal(t, 42, unwrapInt(res))
}

func testHugeGraph(t *testing.T, opts SolverTestOptions) {
	t.Parallel()
	ctx := t.Context()

	cacheManager := newTrackingCacheManager(opts.NewCacheManager(t))

	l := solver.NewSolver(solver.SolverOpt{
		ResolveOpFunc: testOpResolver,
		DefaultCache:  cacheManager,
	})
	defer l.Close()

	j0, err := l.NewJob("j0")
	require.NoError(t, err)

	defer func() {
		if j0 != nil {
			j0.Discard()
		}
	}()

	nodes := 1000

	g, v := generateSubGraph(nodes)
	// PrintGraph(g, "")
	g.Vertex.(*vertexSum).setupCallCounters()

	res, err := j0.Build(ctx, g)
	require.NoError(t, err)
	require.Equal(t, unwrapInt(res), v)
	require.Equal(t, int64(nodes), *g.Vertex.(*vertexSum).cacheCallCount)
	// execCount := *g.Vertex.(*vertexSum).execCallCount
	// require.True(t, execCount < 1000)
	// require.True(t, execCount > 600)
	require.Equal(t, int64(0), cacheManager.loadCounter)

	require.NoError(t, j0.Discard())
	j0 = nil

	j1, err := l.NewJob("j1")
	require.NoError(t, err)

	defer func() {
		if j1 != nil {
			j1.Discard()
		}
	}()

	g.Vertex.(*vertexSum).setupCallCounters()
	res, err = j1.Build(ctx, g)
	require.NoError(t, err)
	require.Equal(t, unwrapInt(res), v)

	require.Equal(t, int64(nodes), *g.Vertex.(*vertexSum).cacheCallCount)
	require.Equal(t, int64(0), *g.Vertex.(*vertexSum).execCallCount)
	require.Equal(t, int64(1), cacheManager.loadCounter)
}

// testOptimizedCacheAccess tests that inputs are not loaded from cache unless
// they are really needed
func testOptimizedCacheAccess(t *testing.T, opts SolverTestOptions) {
	t.Parallel()
	ctx := t.Context()

	cacheManager := newTrackingCacheManager(opts.NewCacheManager(t))

	l := solver.NewSolver(solver.SolverOpt{
		ResolveOpFunc: testOpResolver,
		DefaultCache:  cacheManager,
	})
	defer l.Close()

	j0, err := l.NewJob("j0")
	require.NoError(t, err)

	defer func() {
		if j0 != nil {
			j0.Discard()
		}
	}()

	g0 := solver.Edge{
		Vertex: vtx(vtxOpt{
			name:         "v0",
			cacheKeySeed: "seed0",
			value:        "result0",
			inputs: []solver.Edge{
				{Vertex: vtx(vtxOpt{
					name:         "v1",
					cacheKeySeed: "seed1",
					value:        "result1",
				})},
				{Vertex: vtx(vtxOpt{
					name:         "v2",
					cacheKeySeed: "seed2",
					value:        "result2",
				})},
			},
			slowCacheCompute: map[int]solver.ResultBasedCacheFunc{
				1: digestFromResult,
			},
		}),
	}
	g0.Vertex.(*vertex).setupCallCounters()

	res, err := j0.Build(ctx, g0)
	require.NoError(t, err)
	require.Equal(t, "result0", unwrap(res))

	require.Equal(t, int64(3), *g0.Vertex.(*vertex).cacheCallCount)
	require.Equal(t, int64(3), *g0.Vertex.(*vertex).execCallCount)
	require.Equal(t, int64(0), cacheManager.loadCounter)

	require.NoError(t, j0.Discard())
	j0 = nil

	// changing cache seed for the input with slow cache should not pull result1

	j1, err := l.NewJob("j1")
	require.NoError(t, err)

	defer func() {
		if j1 != nil {
			j1.Discard()
		}
	}()

	g1 := solver.Edge{
		Vertex: vtx(vtxOpt{
			name:         "v0",
			cacheKeySeed: "seed0",
			value:        "result0-nocache",
			inputs: []solver.Edge{
				{Vertex: vtx(vtxOpt{
					name:         "v1",
					cacheKeySeed: "seed1",
					value:        "result1-nocache",
				})},
				{Vertex: vtx(vtxOpt{
					name:         "v2-changed",
					cacheKeySeed: "seed2-changed",
					value:        "result2", // produces same slow key as g0
				})},
			},
			slowCacheCompute: map[int]solver.ResultBasedCacheFunc{
				1: digestFromResult,
			},
		}),
	}
	g1.Vertex.(*vertex).setupCallCounters()

	res, err = j1.Build(ctx, g1)
	require.NoError(t, err)
	require.Equal(t, "result0", unwrap(res))

	require.Equal(t, int64(3), *g1.Vertex.(*vertex).cacheCallCount)
	require.Equal(t, int64(1), *g1.Vertex.(*vertex).execCallCount)
	require.Equal(t, int64(1), cacheManager.loadCounter)

	require.NoError(t, j1.Discard())
	j1 = nil
}

// testOptimizedCacheAccess2 is a more narrow case that tests that inputs are
// not loaded from cache unless they are really needed. Inputs that match by
// definition should be less prioritized for slow cache calculation than the
// inputs that didn't.
func testOptimizedCacheAccess2(t *testing.T, opts SolverTestOptions) {
	t.Parallel()
	ctx := t.Context()

	cacheManager := newTrackingCacheManager(opts.NewCacheManager(t))

	l := solver.NewSolver(solver.SolverOpt{
		ResolveOpFunc: testOpResolver,
		DefaultCache:  cacheManager,
	})
	defer l.Close()

	j0, err := l.NewJob("j0")
	require.NoError(t, err)

	defer func() {
		if j0 != nil {
			j0.Discard()
		}
	}()

	g0 := solver.Edge{
		Vertex: vtx(vtxOpt{
			name:         "v0",
			cacheKeySeed: "seed0",
			value:        "result0",
			inputs: []solver.Edge{
				{Vertex: vtx(vtxOpt{
					name:         "v1",
					cacheKeySeed: "seed1",
					value:        "result1",
				})},
				{Vertex: vtx(vtxOpt{
					name:         "v2",
					cacheKeySeed: "seed2",
					value:        "result2",
				})},
			},
			slowCacheCompute: map[int]solver.ResultBasedCacheFunc{
				0: digestFromResult,
				1: digestFromResult,
			},
		}),
	}
	g0.Vertex.(*vertex).setupCallCounters()

	res, err := j0.Build(ctx, g0)
	require.NoError(t, err)
	require.Equal(t, "result0", unwrap(res))

	require.Equal(t, int64(3), *g0.Vertex.(*vertex).cacheCallCount)
	require.Equal(t, int64(3), *g0.Vertex.(*vertex).execCallCount)
	require.Equal(t, int64(0), cacheManager.loadCounter)

	require.NoError(t, j0.Discard())
	j0 = nil

	// changing cache seed for the input with slow cache should not pull result1

	j1, err := l.NewJob("j1")
	require.NoError(t, err)

	defer func() {
		if j1 != nil {
			j1.Discard()
		}
	}()

	g1 := solver.Edge{
		Vertex: vtx(vtxOpt{
			name:         "v0",
			cacheKeySeed: "seed0",
			value:        "result0-nocache",
			inputs: []solver.Edge{
				{Vertex: vtx(vtxOpt{
					name:         "v1",
					cacheKeySeed: "seed1",
					value:        "result1",
				})},
				{Vertex: vtx(vtxOpt{
					name:         "v2-changed",
					cacheKeySeed: "seed2-changed",
					value:        "result2", // produces same slow key as g0
				})},
			},
			slowCacheCompute: map[int]solver.ResultBasedCacheFunc{
				0: digestFromResult,
				1: digestFromResult,
			},
		}),
	}
	g1.Vertex.(*vertex).setupCallCounters()

	res, err = j1.Build(ctx, g1)
	require.NoError(t, err)
	require.Equal(t, "result0", unwrap(res))

	require.Equal(t, int64(3), *g1.Vertex.(*vertex).cacheCallCount)
	require.Equal(t, int64(1), *g1.Vertex.(*vertex).execCallCount)
	require.Equal(t, int64(1), cacheManager.loadCounter) // v1 is never loaded nor executed

	require.NoError(t, j1.Discard())
	j1 = nil

	// make sure that both inputs are still used for slow cache hit
	j2, err := l.NewJob("j2")
	require.NoError(t, err)

	defer func() {
		if j2 != nil {
			j2.Discard()
		}
	}()

	g2 := solver.Edge{
		Vertex: vtx(vtxOpt{
			name:         "v0",
			cacheKeySeed: "seed0",
			value:        "result0-nocache",
			inputs: []solver.Edge{
				{Vertex: vtx(vtxOpt{
					name:         "v1",
					cacheKeySeed: "seed1-changed2",
					value:        "result1",
				})},
				{Vertex: vtx(vtxOpt{
					name:         "v2-changed",
					cacheKeySeed: "seed2-changed2",
					value:        "result2", // produces same slow key as g0
				})},
			},
			slowCacheCompute: map[int]solver.ResultBasedCacheFunc{
				0: digestFromResult,
				1: digestFromResult,
			},
		}),
	}
	g2.Vertex.(*vertex).setupCallCounters()

	res, err = j2.Build(ctx, g2)
	require.NoError(t, err)
	require.Equal(t, "result0", unwrap(res))

	require.Equal(t, int64(3), *g2.Vertex.(*vertex).cacheCallCount)
	require.Equal(t, int64(2), *g2.Vertex.(*vertex).execCallCount)
	require.Equal(t, int64(2), cacheManager.loadCounter)

	require.NoError(t, j2.Discard())
	j1 = nil
}

func testSlowCache(t *testing.T, opts SolverTestOptions) {
	t.Parallel()
	ctx := t.Context()

	l := solver.NewSolver(solver.SolverOpt{
		ResolveOpFunc: testOpResolver,
		DefaultCache:  opts.NewCacheManager(t),
	})
	defer l.Close()

	j0, err := l.NewJob("j0")
	require.NoError(t, err)

	defer func() {
		if j0 != nil {
			j0.Discard()
		}
	}()

	g0 := solver.Edge{
		Vertex: vtx(vtxOpt{
			name:         "v0",
			cacheKeySeed: "seed0",
			value:        "result0",
			inputs: []solver.Edge{
				{Vertex: vtx(vtxOpt{
					name:         "v1",
					cacheKeySeed: "seed1",
					value:        "result1",
				})},
			},
			slowCacheCompute: map[int]solver.ResultBasedCacheFunc{
				0: digestFromResult,
			},
		}),
	}

	res, err := j0.Build(ctx, g0)
	require.NoError(t, err)
	require.Equal(t, "result0", unwrap(res))

	require.NoError(t, j0.Discard())
	j0 = nil

	j1, err := l.NewJob("j1")
	require.NoError(t, err)

	defer func() {
		if j1 != nil {
			j1.Discard()
		}
	}()

	g1 := solver.Edge{
		Vertex: vtx(vtxOpt{
			name:         "v2",
			cacheKeySeed: "seed0",
			value:        "not-cached",
			inputs: []solver.Edge{
				{Vertex: vtx(vtxOpt{
					name:         "v3",
					cacheKeySeed: "seed3",
					value:        "result1", // used for slow key
				})},
			},
			slowCacheCompute: map[int]solver.ResultBasedCacheFunc{
				0: digestFromResult,
			},
		}),
	}

	res, err = j1.Build(ctx, g1)
	require.NoError(t, err)
	require.Equal(t, "result0", unwrap(res))

	require.NoError(t, j1.Discard())
	j1 = nil
}

func testSlowCacheErrorResultReleasedWithJob(t *testing.T, opts SolverTestOptions) {
	t.Parallel()
	ctx := t.Context()

	l := solver.NewSolver(solver.SolverOpt{
		ResolveOpFunc: testOpResolver,
		DefaultCache:  opts.NewCacheManager(t),
	})
	defer l.Close()

	j, err := l.NewJob("j0")
	require.NoError(t, err)
	defer func() {
		if j != nil {
			require.NoError(t, j.Discard())
		}
	}()

	var releaseCount atomic.Int64

	inputRes := &countedResult{
		id:           identity.NewID(),
		value:        "input",
		releaseCount: &releaseCount,
	}

	g := solver.Edge{
		Vertex: vtx(vtxOpt{
			name:         "v0",
			cacheKeySeed: "seed0",
			value:        "result0",
			inputs: []solver.Edge{
				{Vertex: vtx(vtxOpt{
					name:         "v1",
					cacheKeySeed: "seed1",
					result:       inputRes,
				})},
			},
			slowCacheCompute: map[int]solver.ResultBasedCacheFunc{
				0: func(context.Context, solver.Result, session.Group) (digest.Digest, error) {
					return "", errors.New("slow cache error")
				},
			},
		}),
	}

	_, err = j.Build(ctx, g)
	require.Error(t, err)

	var sce *solver.SlowCacheError
	require.ErrorAs(t, err, &sce)
	require.NotNil(t, sce.Result)

	require.Never(t, func() bool {
		return releaseCount.Load() != 0
	}, 100*time.Millisecond, 10*time.Millisecond)

	require.NoError(t, j.Discard())
	j = nil

	require.Eventually(t, func() bool {
		return releaseCount.Load() == 1
	}, time.Second, 10*time.Millisecond)
}

// testParallelInputs validates that inputs are processed in parallel
func testParallelInputs(t *testing.T, opts SolverTestOptions) {
	t.Parallel()
	ctx := t.Context()

	l := solver.NewSolver(solver.SolverOpt{
		ResolveOpFunc: testOpResolver,
		DefaultCache:  opts.NewCacheManager(t),
	})
	defer l.Close()

	j0, err := l.NewJob("j0")
	require.NoError(t, err)

	defer func() {
		if j0 != nil {
			j0.Discard()
		}
	}()

	wait2Ready := blockingFunction(2)
	wait2Ready2 := blockingFunction(2)

	g0 := solver.Edge{
		Vertex: vtx(vtxOpt{
			name:         "v0",
			cacheKeySeed: "seed0",
			value:        "result0",
			inputs: []solver.Edge{
				{Vertex: vtx(vtxOpt{
					name:         "v1",
					cacheKeySeed: "seed1",
					value:        "result1",
					cachePreFunc: wait2Ready,
					execPreFunc:  wait2Ready2,
				})},
				{Vertex: vtx(vtxOpt{
					name:         "v2",
					cacheKeySeed: "seed2",
					value:        "result2",
					cachePreFunc: wait2Ready,
					execPreFunc:  wait2Ready2,
				})},
			},
		}),
	}
	g0.Vertex.(*vertex).setupCallCounters()

	res, err := j0.Build(ctx, g0)
	require.NoError(t, err)
	require.Equal(t, "result0", unwrap(res))
	require.NoError(t, j0.Discard())
	j0 = nil

	require.Equal(t, int64(3), *g0.Vertex.(*vertex).cacheCallCount)
	require.Equal(t, int64(3), *g0.Vertex.(*vertex).execCallCount)
}

func testErrorReturns(t *testing.T, opts SolverTestOptions) {
	t.Parallel()
	ctx := t.Context()

	l := solver.NewSolver(solver.SolverOpt{
		ResolveOpFunc: testOpResolver,
		DefaultCache:  opts.NewCacheManager(t),
	})
	defer l.Close()

	j0, err := l.NewJob("j0")
	require.NoError(t, err)

	defer func() {
		if j0 != nil {
			j0.Discard()
		}
	}()

	g0 := solver.Edge{
		Vertex: vtx(vtxOpt{
			name:         "v0",
			cacheKeySeed: "seed0",
			value:        "result0",
			inputs: []solver.Edge{
				{Vertex: vtx(vtxOpt{
					name:         "v1",
					cacheKeySeed: "seed1",
					value:        "result1",
					cachePreFunc: func(ctx context.Context) error {
						return errors.New("error-from-test")
					},
				})},
				{Vertex: vtx(vtxOpt{
					name:         "v2",
					cacheKeySeed: "seed2",
					value:        "result2",
				})},
			},
		}),
	}

	_, err = j0.Build(ctx, g0)
	require.Error(t, err)
	require.Contains(t, err.Error(), "error-from-test")

	require.NoError(t, j0.Discard())
	j0 = nil

	// error with cancel error. to check that this isn't mixed up with regular build cancel.

	j1, err := l.NewJob("j1")
	require.NoError(t, err)

	defer func() {
		if j1 != nil {
			j1.Discard()
		}
	}()

	g1 := solver.Edge{
		Vertex: vtx(vtxOpt{
			name:         "v0",
			cacheKeySeed: "seed0",
			value:        "result0",
			inputs: []solver.Edge{
				{Vertex: vtx(vtxOpt{
					name:         "v1",
					cacheKeySeed: "seed1",
					value:        "result1",
					cachePreFunc: func(ctx context.Context) error {
						return context.Canceled
					},
				})},
				{Vertex: vtx(vtxOpt{
					name:         "v2",
					cacheKeySeed: "seed2",
					value:        "result2",
				})},
			},
		}),
	}

	_, err = j1.Build(ctx, g1)
	require.Error(t, err)
	require.Equal(t, true, errors.Is(err, context.Canceled))

	require.NoError(t, j1.Discard())
	j1 = nil

	// error from exec

	j2, err := l.NewJob("j2")
	require.NoError(t, err)

	defer func() {
		if j2 != nil {
			j2.Discard()
		}
	}()

	g2 := solver.Edge{
		Vertex: vtx(vtxOpt{
			name:         "v0",
			cacheKeySeed: "seed0",
			value:        "result0",
			inputs: []solver.Edge{
				{Vertex: vtx(vtxOpt{
					name:         "v1",
					cacheKeySeed: "seed1",
					value:        "result1",
				})},
				{Vertex: vtx(vtxOpt{
					name:         "v2",
					cacheKeySeed: "seed3",
					value:        "result2",
					execPreFunc: func(ctx context.Context) error {
						return errors.New("exec-error-from-test")
					},
				})},
			},
		}),
	}

	_, err = j2.Build(ctx, g2)
	require.Error(t, err)
	require.Contains(t, err.Error(), "exec-error-from-test")

	require.NoError(t, j2.Discard())
	j1 = nil
}

func testMultipleCacheSources(t *testing.T, opts SolverTestOptions) {
	t.Parallel()
	ctx := t.Context()

	cacheManager := newTrackingCacheManager(opts.NewCacheManager(t))

	l := solver.NewSolver(solver.SolverOpt{
		ResolveOpFunc: testOpResolver,
		DefaultCache:  cacheManager,
	})
	defer l.Close()

	j0, err := l.NewJob("j0")
	require.NoError(t, err)

	defer func() {
		if j0 != nil {
			j0.Discard()
		}
	}()

	g0 := solver.Edge{
		Vertex: vtx(vtxOpt{
			name:         "v0",
			cacheKeySeed: "seed0",
			value:        "result0",
			inputs: []solver.Edge{
				{Vertex: vtx(vtxOpt{
					name:         "v1",
					cacheKeySeed: "seed1",
					value:        "result1",
				})},
			},
		}),
	}

	res, err := j0.Build(ctx, g0)
	require.NoError(t, err)
	require.Equal(t, "result0", unwrap(res))
	require.Equal(t, int64(0), cacheManager.loadCounter)

	require.NoError(t, j0.Discard())
	j0 = nil

	cacheManager2 := newTrackingCacheManager(solver.NewInMemoryCacheManager())

	l2 := solver.NewSolver(solver.SolverOpt{
		ResolveOpFunc: testOpResolver,
		DefaultCache:  cacheManager2,
	})
	defer l2.Close()

	j1, err := l.NewJob("j1")
	require.NoError(t, err)

	defer func() {
		if j1 != nil {
			j1.Discard()
		}
	}()

	g1 := solver.Edge{
		Vertex: vtx(vtxOpt{
			name:         "v0",
			cacheKeySeed: "seed0",
			value:        "result0-no-cache",
			cacheSource:  cacheManager,
			inputs: []solver.Edge{
				{Vertex: vtx(vtxOpt{
					name:         "v1",
					cacheKeySeed: "seed1",
					value:        "result1-no-cache",
					cacheSource:  cacheManager,
				})},
			},
		}),
	}

	res, err = j1.Build(ctx, g1)
	require.NoError(t, err)
	require.Equal(t, "result0", unwrap(res))

	require.Equal(t, int64(1), cacheManager.loadCounter)
	require.Equal(t, int64(0), cacheManager2.loadCounter)

	require.NoError(t, j1.Discard())
	j0 = nil

	// build on top of old cache
	j2, err := l.NewJob("j2")
	require.NoError(t, err)

	defer func() {
		if j2 != nil {
			j2.Discard()
		}
	}()

	g2 := solver.Edge{
		Vertex: vtx(vtxOpt{
			name:         "v2",
			cacheKeySeed: "seed2",
			value:        "result2",
			inputs:       []solver.Edge{g1},
		}),
	}

	res, err = j1.Build(ctx, g2)
	require.NoError(t, err)
	require.Equal(t, "result2", unwrap(res))

	require.Equal(t, int64(2), cacheManager.loadCounter)
	require.Equal(t, int64(0), cacheManager2.loadCounter)

	require.NoError(t, j1.Discard())
	j1 = nil
}

func testRepeatBuildWithIgnoreCache(t *testing.T, opts SolverTestOptions) {
	t.Parallel()
	ctx := t.Context()

	l := solver.NewSolver(solver.SolverOpt{
		ResolveOpFunc: testOpResolver,
		DefaultCache:  opts.NewCacheManager(t),
	})
	defer l.Close()

	j0, err := l.NewJob("j0")
	require.NoError(t, err)

	defer func() {
		if j0 != nil {
			j0.Discard()
		}
	}()

	g0 := solver.Edge{
		Vertex: vtx(vtxOpt{
			name:         "v0",
			cacheKeySeed: "seed0",
			value:        "result0",
			inputs: []solver.Edge{
				{Vertex: vtx(vtxOpt{
					name:         "v1",
					cacheKeySeed: "seed1",
					value:        "result1",
				})},
			},
		}),
	}
	g0.Vertex.(*vertex).setupCallCounters()

	res, err := j0.Build(ctx, g0)
	require.NoError(t, err)
	require.Equal(t, "result0", unwrap(res))

	require.Equal(t, int64(2), *g0.Vertex.(*vertex).cacheCallCount)
	require.Equal(t, int64(2), *g0.Vertex.(*vertex).execCallCount)

	require.NoError(t, j0.Discard())
	j0 = nil

	// rebuild with ignore-cache reevaluates everything

	j1, err := l.NewJob("j1")
	require.NoError(t, err)

	defer func() {
		if j1 != nil {
			j1.Discard()
		}
	}()

	g1 := solver.Edge{
		Vertex: vtx(vtxOpt{
			name:         "v0",
			cacheKeySeed: "seed0",
			value:        "result0-1",
			ignoreCache:  true,
			inputs: []solver.Edge{
				{Vertex: vtx(vtxOpt{
					name:         "v1",
					cacheKeySeed: "seed1",
					value:        "result1-1",
					ignoreCache:  true,
				})},
			},
		}),
	}
	g1.Vertex.(*vertex).setupCallCounters()

	res, err = j1.Build(ctx, g1)
	require.NoError(t, err)
	require.Equal(t, "result0-1", unwrap(res))

	require.Equal(t, int64(2), *g1.Vertex.(*vertex).cacheCallCount)
	require.Equal(t, int64(2), *g1.Vertex.(*vertex).execCallCount)

	require.NoError(t, j1.Discard())
	j1 = nil

	// ignore-cache in child reevaluates parent

	j2, err := l.NewJob("j2")
	require.NoError(t, err)

	defer func() {
		if j2 != nil {
			j2.Discard()
		}
	}()

	g2 := solver.Edge{
		Vertex: vtx(vtxOpt{
			name:         "v0",
			cacheKeySeed: "seed0",
			value:        "result0-2",
			inputs: []solver.Edge{
				{Vertex: vtx(vtxOpt{
					name:         "v1",
					cacheKeySeed: "seed1",
					value:        "result1-2",
					ignoreCache:  true,
				})},
			},
		}),
	}
	g2.Vertex.(*vertex).setupCallCounters()

	res, err = j2.Build(ctx, g2)
	require.NoError(t, err)
	require.Equal(t, "result0-2", unwrap(res))

	require.Equal(t, int64(2), *g2.Vertex.(*vertex).cacheCallCount)
	require.Equal(t, int64(2), *g2.Vertex.(*vertex).execCallCount)

	require.NoError(t, j2.Discard())
	j2 = nil
}

// testIgnoreCacheResumeFromSlowCache tests that parent cache resumes if child
// with ignore-cache generates same slow cache key
func testIgnoreCacheResumeFromSlowCache(t *testing.T, opts SolverTestOptions) {
	t.Parallel()
	ctx := t.Context()

	l := solver.NewSolver(solver.SolverOpt{
		ResolveOpFunc: testOpResolver,
		DefaultCache:  opts.NewCacheManager(t),
	})
	defer l.Close()

	j0, err := l.NewJob("j0")
	require.NoError(t, err)

	defer func() {
		if j0 != nil {
			j0.Discard()
		}
	}()

	g0 := solver.Edge{
		Vertex: vtx(vtxOpt{
			name:         "v0",
			cacheKeySeed: "seed0",
			value:        "result0",
			slowCacheCompute: map[int]solver.ResultBasedCacheFunc{
				0: digestFromResult,
			},
			inputs: []solver.Edge{
				{Vertex: vtx(vtxOpt{
					name:         "v1",
					cacheKeySeed: "seed1",
					value:        "result1",
				})},
			},
		}),
	}
	g0.Vertex.(*vertex).setupCallCounters()

	res, err := j0.Build(ctx, g0)
	require.NoError(t, err)
	require.Equal(t, "result0", unwrap(res))

	require.Equal(t, int64(2), *g0.Vertex.(*vertex).cacheCallCount)
	require.Equal(t, int64(2), *g0.Vertex.(*vertex).execCallCount)

	require.NoError(t, j0.Discard())
	j0 = nil

	// rebuild reevaluates child, but not parent

	j1, err := l.NewJob("j1")
	require.NoError(t, err)

	defer func() {
		if j1 != nil {
			j1.Discard()
		}
	}()

	g1 := solver.Edge{
		Vertex: vtx(vtxOpt{
			name:         "v0-1", // doesn't matter but avoid match because another bug
			cacheKeySeed: "seed0",
			value:        "result0-no-cache",
			slowCacheCompute: map[int]solver.ResultBasedCacheFunc{
				0: digestFromResult,
			},
			inputs: []solver.Edge{
				{Vertex: vtx(vtxOpt{
					name:         "v1-1",
					cacheKeySeed: "seed1-1", // doesn't matter but avoid match because another bug
					value:        "result1", // same as g0
					ignoreCache:  true,
				})},
			},
		}),
	}
	g1.Vertex.(*vertex).setupCallCounters()

	res, err = j1.Build(ctx, g1)
	require.NoError(t, err)
	require.Equal(t, "result0", unwrap(res))

	require.Equal(t, int64(2), *g1.Vertex.(*vertex).cacheCallCount)
	require.Equal(t, int64(1), *g1.Vertex.(*vertex).execCallCount)

	require.NoError(t, j1.Discard())
	j1 = nil
}

func testParallelBuildsIgnoreCache(t *testing.T, opts SolverTestOptions) {
	t.Parallel()
	ctx := t.Context()

	l := solver.NewSolver(solver.SolverOpt{
		ResolveOpFunc: testOpResolver,
		DefaultCache:  opts.NewCacheManager(t),
	})
	defer l.Close()

	j0, err := l.NewJob("j0")
	require.NoError(t, err)

	defer func() {
		if j0 != nil {
			j0.Discard()
		}
	}()

	g0 := solver.Edge{
		Vertex: vtx(vtxOpt{
			name:         "v0",
			cacheKeySeed: "seed0",
			value:        "result0",
		}),
	}
	g0.Vertex.(*vertex).setupCallCounters()

	res, err := j0.Build(ctx, g0)
	require.NoError(t, err)
	require.Equal(t, "result0", unwrap(res))

	// match by vertex digest
	j1, err := l.NewJob("j1")
	require.NoError(t, err)

	defer func() {
		if j1 != nil {
			j1.Discard()
		}
	}()

	g1 := solver.Edge{
		Vertex: vtx(vtxOpt{
			name:         "v0",
			cacheKeySeed: "seed1",
			value:        "result1",
			ignoreCache:  true,
		}),
	}
	g1.Vertex.(*vertex).setupCallCounters()

	res, err = j1.Build(ctx, g1)
	require.NoError(t, err)
	require.Equal(t, "result1", unwrap(res))

	require.NoError(t, j0.Discard())
	j0 = nil
	require.NoError(t, j1.Discard())
	j1 = nil

	// new base
	j2, err := l.NewJob("j2")
	require.NoError(t, err)

	defer func() {
		if j2 != nil {
			j2.Discard()
		}
	}()

	g2 := solver.Edge{
		Vertex: vtx(vtxOpt{
			name:         "v2",
			cacheKeySeed: "seed2",
			value:        "result2",
		}),
	}
	g2.Vertex.(*vertex).setupCallCounters()

	res, err = j2.Build(ctx, g2)
	require.NoError(t, err)
	require.Equal(t, "result2", unwrap(res))

	// match by cache key
	j3, err := l.NewJob("j3")
	require.NoError(t, err)

	defer func() {
		if j3 != nil {
			j3.Discard()
		}
	}()

	g3 := solver.Edge{
		Vertex: vtx(vtxOpt{
			name:         "v3",
			cacheKeySeed: "seed2",
			value:        "result3",
			ignoreCache:  true,
		}),
	}
	g3.Vertex.(*vertex).setupCallCounters()

	res, err = j3.Build(ctx, g3)
	require.NoError(t, err)
	require.Equal(t, "result3", unwrap(res))

	// add another ignorecache merges now

	j4, err := l.NewJob("j4")
	require.NoError(t, err)

	defer func() {
		if j4 != nil {
			j4.Discard()
		}
	}()

	g4 := solver.Edge{
		Vertex: vtx(vtxOpt{
			name:         "v4",
			cacheKeySeed: "seed2", // same as g2/g3
			value:        "result4",
			ignoreCache:  true,
		}),
	}
	g4.Vertex.(*vertex).setupCallCounters()

	res, err = j4.Build(ctx, g4)
	require.NoError(t, err)
	require.Equal(t, "result3", unwrap(res))

	// add another !ignorecache merges now

	j5, err := l.NewJob("j5")
	require.NoError(t, err)

	defer func() {
		if j5 != nil {
			j5.Discard()
		}
	}()

	g5 := solver.Edge{
		Vertex: vtx(vtxOpt{
			name:         "v5",
			cacheKeySeed: "seed2", // same as g2/g3/g4
			value:        "result5",
		}),
	}
	g5.Vertex.(*vertex).setupCallCounters()

	res, err = j5.Build(ctx, g5)
	require.NoError(t, err)
	require.Equal(t, "result3", unwrap(res))
}

func testSubbuild(t *testing.T, opts SolverTestOptions) {
	t.Parallel()
	ctx := t.Context()

	l := solver.NewSolver(solver.SolverOpt{
		ResolveOpFunc: testOpResolver,
		DefaultCache:  opts.NewCacheManager(t),
	})
	defer l.Close()

	j0, err := l.NewJob("j0")
	require.NoError(t, err)

	defer func() {
		if j0 != nil {
			j0.Discard()
		}
	}()

	g0 := solver.Edge{
		Vertex: vtxSum(1, vtxOpt{
			inputs: []solver.Edge{
				{Vertex: vtxSubBuild(solver.Edge{Vertex: vtxConst(7, vtxOpt{})}, vtxOpt{
					cacheKeySeed: "seed0",
				})},
			},
		}),
	}
	g0.Vertex.(*vertexSum).setupCallCounters()

	res, err := j0.Build(ctx, g0)
	require.NoError(t, err)
	require.Equal(t, 8, unwrapInt(res))

	require.Equal(t, int64(2), *g0.Vertex.(*vertexSum).cacheCallCount)
	require.Equal(t, int64(2), *g0.Vertex.(*vertexSum).execCallCount)

	require.NoError(t, j0.Discard())
	j0 = nil

	j1, err := l.NewJob("j1")
	require.NoError(t, err)

	defer func() {
		if j1 != nil {
			j1.Discard()
		}
	}()

	g0.Vertex.(*vertexSum).setupCallCounters()

	res, err = j1.Build(ctx, g0)
	require.NoError(t, err)
	require.Equal(t, 8, unwrapInt(res))

	require.Equal(t, int64(2), *g0.Vertex.(*vertexSum).cacheCallCount)
	require.Equal(t, int64(0), *g0.Vertex.(*vertexSum).execCallCount)

	require.NoError(t, j1.Discard())
	j1 = nil
}

func testCacheWithSelector(t *testing.T, opts SolverTestOptions) {
	t.Parallel()
	ctx := t.Context()

	cacheManager := newTrackingCacheManager(opts.NewCacheManager(t))

	l := solver.NewSolver(solver.SolverOpt{
		ResolveOpFunc: testOpResolver,
		DefaultCache:  cacheManager,
	})
	defer l.Close()

	j0, err := l.NewJob("j0")
	require.NoError(t, err)

	defer func() {
		if j0 != nil {
			j0.Discard()
		}
	}()

	g0 := solver.Edge{
		Vertex: vtx(vtxOpt{
			name:         "v0",
			cacheKeySeed: "seed0",
			value:        "result0",
			inputs: []solver.Edge{
				{Vertex: vtx(vtxOpt{
					name:         "v1",
					cacheKeySeed: "seed1",
					value:        "result1",
				})},
			},
			selectors: map[int]digest.Digest{
				0: dgst("sel0"),
			},
		}),
	}
	g0.Vertex.(*vertex).setupCallCounters()

	res, err := j0.Build(ctx, g0)
	require.NoError(t, err)
	require.Equal(t, "result0", unwrap(res))

	require.Equal(t, int64(2), *g0.Vertex.(*vertex).cacheCallCount)
	require.Equal(t, int64(2), *g0.Vertex.(*vertex).execCallCount)
	require.Equal(t, int64(0), cacheManager.loadCounter)

	require.NoError(t, j0.Discard())
	j0 = nil

	// repeat, cache is matched

	j1, err := l.NewJob("j1")
	require.NoError(t, err)

	defer func() {
		if j1 != nil {
			j1.Discard()
		}
	}()

	g1 := solver.Edge{
		Vertex: vtx(vtxOpt{
			name:         "v0",
			cacheKeySeed: "seed0",
			value:        "result0-no-cache",
			inputs: []solver.Edge{
				{Vertex: vtx(vtxOpt{
					name:         "v1",
					cacheKeySeed: "seed1",
					value:        "result1-no-cache",
				})},
			},
			selectors: map[int]digest.Digest{
				0: dgst("sel0"),
			},
		}),
	}
	g1.Vertex.(*vertex).setupCallCounters()

	res, err = j1.Build(ctx, g1)
	require.NoError(t, err)
	require.Equal(t, "result0", unwrap(res))

	require.Equal(t, int64(2), *g1.Vertex.(*vertex).cacheCallCount)
	require.Equal(t, int64(0), *g1.Vertex.(*vertex).execCallCount)
	require.Equal(t, int64(1), cacheManager.loadCounter)

	require.NoError(t, j1.Discard())
	j1 = nil

	// using different selector doesn't match

	j2, err := l.NewJob("j2")
	require.NoError(t, err)

	defer func() {
		if j2 != nil {
			j2.Discard()
		}
	}()

	g2 := solver.Edge{
		Vertex: vtx(vtxOpt{
			name:         "v0",
			cacheKeySeed: "seed0",
			value:        "result0-1",
			inputs: []solver.Edge{
				{Vertex: vtx(vtxOpt{
					name:         "v1",
					cacheKeySeed: "seed1",
					value:        "result1-1",
				})},
			},
			selectors: map[int]digest.Digest{
				0: dgst("sel1"),
			},
		}),
	}
	g2.Vertex.(*vertex).setupCallCounters()

	res, err = j2.Build(ctx, g2)
	require.NoError(t, err)
	require.Equal(t, "result0-1", unwrap(res))

	require.Equal(t, int64(2), *g2.Vertex.(*vertex).cacheCallCount)
	require.Equal(t, int64(1), *g2.Vertex.(*vertex).execCallCount)
	require.Equal(t, int64(2), cacheManager.loadCounter)

	require.NoError(t, j2.Discard())
	j2 = nil
}

func testCacheSlowWithSelector(t *testing.T, opts SolverTestOptions) {
	t.Parallel()
	ctx := t.Context()

	cacheManager := newTrackingCacheManager(opts.NewCacheManager(t))

	l := solver.NewSolver(solver.SolverOpt{
		ResolveOpFunc: testOpResolver,
		DefaultCache:  cacheManager,
	})
	defer l.Close()

	j0, err := l.NewJob("j0")
	require.NoError(t, err)

	defer func() {
		if j0 != nil {
			j0.Discard()
		}
	}()

	g0 := solver.Edge{
		Vertex: vtx(vtxOpt{
			name:         "v0",
			cacheKeySeed: "seed0",
			value:        "result0",
			inputs: []solver.Edge{
				{Vertex: vtx(vtxOpt{
					name:         "v1",
					cacheKeySeed: "seed1",
					value:        "result1",
				})},
			},
			selectors: map[int]digest.Digest{
				0: dgst("sel0"),
			},
			slowCacheCompute: map[int]solver.ResultBasedCacheFunc{
				0: digestFromResult,
			},
		}),
	}
	g0.Vertex.(*vertex).setupCallCounters()

	res, err := j0.Build(ctx, g0)
	require.NoError(t, err)
	require.Equal(t, "result0", unwrap(res))

	require.Equal(t, int64(2), *g0.Vertex.(*vertex).cacheCallCount)
	require.Equal(t, int64(2), *g0.Vertex.(*vertex).execCallCount)
	require.Equal(t, int64(0), cacheManager.loadCounter)

	require.NoError(t, j0.Discard())
	j0 = nil

	// repeat, cache is matched

	j1, err := l.NewJob("j1")
	require.NoError(t, err)

	defer func() {
		if j1 != nil {
			j1.Discard()
		}
	}()

	g1 := solver.Edge{
		Vertex: vtx(vtxOpt{
			name:         "v0",
			cacheKeySeed: "seed0",
			value:        "result0-no-cache",
			inputs: []solver.Edge{
				{Vertex: vtx(vtxOpt{
					name:         "v1",
					cacheKeySeed: "seed1",
					value:        "result1-no-cache",
				})},
			},
			selectors: map[int]digest.Digest{
				0: dgst("sel1"),
			},
			slowCacheCompute: map[int]solver.ResultBasedCacheFunc{
				0: digestFromResult,
			},
		}),
	}
	g1.Vertex.(*vertex).setupCallCounters()

	res, err = j1.Build(ctx, g1)
	require.NoError(t, err)
	require.Equal(t, "result0", unwrap(res))

	require.Equal(t, int64(2), *g1.Vertex.(*vertex).cacheCallCount)
	require.Equal(t, int64(0), *g1.Vertex.(*vertex).execCallCount)
	require.Equal(t, int64(2), cacheManager.loadCounter)

	require.NoError(t, j1.Discard())
	j1 = nil
}

func testCacheExporting(t *testing.T, opts SolverTestOptions) {
	t.Parallel()
	ctx := t.Context()

	cacheManager := newTrackingCacheManager(opts.NewCacheManager(t))

	l := solver.NewSolver(solver.SolverOpt{
		ResolveOpFunc: testOpResolver,
		DefaultCache:  cacheManager,
	})
	defer l.Close()

	j0, err := l.NewJob("j0")
	require.NoError(t, err)

	defer func() {
		if j0 != nil {
			j0.Discard()
		}
	}()

	g0 := solver.Edge{
		Vertex: vtxSum(1, vtxOpt{
			inputs: []solver.Edge{
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

	_, err = res.CacheKeys()[0].Exporter.ExportTo(ctx, expTarget, testExporterOpts(true))
	require.NoError(t, err)

	require.Equal(t, 3, expTarget.numUniqueRecords())
	require.Equal(t, 0, expTarget.records[0].results)
	require.Equal(t, 0, expTarget.records[1].results)
	require.Equal(t, 1, expTarget.records[5].results)
	require.Equal(t, 0, expTarget.records[0].links)
	require.Equal(t, 0, expTarget.records[1].links)
	require.Equal(t, 2, expTarget.records[5].links)

	j1, err := l.NewJob("j1")
	require.NoError(t, err)

	defer func() {
		if j1 != nil {
			j1.Discard()
		}
	}()

	res, err = j1.Build(ctx, g0)
	require.NoError(t, err)
	require.Equal(t, 6, unwrapInt(res))

	require.NoError(t, j1.Discard())
	j1 = nil

	expTarget = newTestExporterTarget()

	_, err = res.CacheKeys()[0].Exporter.ExportTo(ctx, expTarget, testExporterOpts(true))
	require.NoError(t, err)

	// the order of the records isn't really significant
	require.Equal(t, 3, expTarget.numUniqueRecords())
	require.Equal(t, 0, expTarget.records[0].results)
	require.Equal(t, 0, expTarget.records[1].results)
	require.Equal(t, 1, expTarget.records[5].results)
	require.Equal(t, 0, expTarget.records[0].links)
	require.Equal(t, 0, expTarget.records[1].links)
	require.Equal(t, 2, expTarget.records[5].links)
}

func testCacheExportingModeMin(t *testing.T, opts SolverTestOptions) {
	t.Parallel()
	ctx := t.Context()

	cacheManager := newTrackingCacheManager(opts.NewCacheManager(t))

	l := solver.NewSolver(solver.SolverOpt{
		ResolveOpFunc: testOpResolver,
		DefaultCache:  cacheManager,
	})
	defer l.Close()

	j0, err := l.NewJob("j0")
	require.NoError(t, err)

	defer func() {
		if j0 != nil {
			j0.Discard()
		}
	}()

	g0 := solver.Edge{
		Vertex: vtxSum(1, vtxOpt{
			inputs: []solver.Edge{
				{Vertex: vtxSum(2, vtxOpt{
					inputs: []solver.Edge{
						{Vertex: vtxConst(3, vtxOpt{})},
					},
				})},
				{Vertex: vtxConst(5, vtxOpt{})},
			},
		}),
	}

	res, err := j0.Build(ctx, g0)
	require.NoError(t, err)
	require.Equal(t, 11, unwrapInt(res))

	require.NoError(t, j0.Discard())
	j0 = nil

	expTarget := newTestExporterTarget()

	_, err = res.CacheKeys()[0].Exporter.ExportTo(ctx, expTarget, testExporterOpts(false))
	require.NoError(t, err)

	require.Equal(t, 4, expTarget.numUniqueRecords())
	require.Equal(t, 0, expTarget.records[0].results)
	require.Equal(t, 0, expTarget.records[1].results)
	require.Equal(t, 0, expTarget.records[2].results)
	require.Equal(t, 0, expTarget.records[3].results)
	require.Equal(t, 1, expTarget.records[7].results)
	require.Equal(t, 0, expTarget.records[0].links)
	require.Equal(t, 0, expTarget.records[1].links)
	require.Equal(t, 1, expTarget.records[2].links)
	require.Equal(t, 2, expTarget.records[7].links)

	j1, err := l.NewJob("j1")
	require.NoError(t, err)

	defer func() {
		if j1 != nil {
			j1.Discard()
		}
	}()

	res, err = j1.Build(ctx, g0)
	require.NoError(t, err)
	require.Equal(t, 11, unwrapInt(res))

	require.NoError(t, j1.Discard())
	j1 = nil

	expTarget = newTestExporterTarget()

	_, err = res.CacheKeys()[0].Exporter.ExportTo(ctx, expTarget, testExporterOpts(false))
	require.NoError(t, err)

	// the order of the records isn't really significant
	require.Equal(t, 4, expTarget.numUniqueRecords())
	require.Equal(t, 0, expTarget.records[0].results)
	require.Equal(t, 0, expTarget.records[1].results)
	require.Equal(t, 0, expTarget.records[2].results)
	require.Equal(t, 1, expTarget.records[7].results)
	require.Equal(t, 0, expTarget.records[0].links)
	require.Equal(t, 0, expTarget.records[1].links)
	require.Equal(t, 1, expTarget.records[2].links)
	require.Equal(t, 1, expTarget.records[3].links)
	require.Equal(t, 2, expTarget.records[6].links)
	require.Equal(t, 2, expTarget.records[7].links)

	// one more check with all mode
	j2, err := l.NewJob("j2")
	require.NoError(t, err)

	defer func() {
		if j2 != nil {
			j2.Discard()
		}
	}()

	res, err = j2.Build(ctx, g0)
	require.NoError(t, err)
	require.Equal(t, 11, unwrapInt(res))

	require.NoError(t, j2.Discard())
	j2 = nil

	expTarget = newTestExporterTarget()

	_, err = res.CacheKeys()[0].Exporter.ExportTo(ctx, expTarget, testExporterOpts(true))
	require.NoError(t, err)

	// the order of the records isn't really significant
	require.Equal(t, 4, expTarget.numUniqueRecords())
	require.Equal(t, 0, expTarget.records[0].results)
	require.Equal(t, 0, expTarget.records[1].results)
	require.Equal(t, 0, expTarget.records[2].results)
	require.Equal(t, 1, expTarget.records[3].results)
	require.Equal(t, 1, expTarget.records[7].results)
	require.Equal(t, 0, expTarget.records[0].links)
	require.Equal(t, 0, expTarget.records[1].links)
	require.Equal(t, 1, expTarget.records[2].links)
	require.Equal(t, 2, expTarget.records[6].links)
}

func testSlowCacheAvoidAccess(t *testing.T, opts SolverTestOptions) {
	t.Parallel()
	ctx := t.Context()

	cacheManager := newTrackingCacheManager(opts.NewCacheManager(t))

	l := solver.NewSolver(solver.SolverOpt{
		ResolveOpFunc: testOpResolver,
		DefaultCache:  cacheManager,
	})
	defer l.Close()

	j0, err := l.NewJob("j0")
	require.NoError(t, err)

	defer func() {
		if j0 != nil {
			j0.Discard()
		}
	}()

	g0 := solver.Edge{
		Vertex: vtx(vtxOpt{
			name:         "v0",
			cacheKeySeed: "seed0",
			cachePreFunc: func(context.Context) error {
				select {
				case <-time.After(50 * time.Millisecond):
				case <-ctx.Done():
				}
				return nil
			},
			value: "result0",
			inputs: []solver.Edge{{
				Vertex: vtx(vtxOpt{
					name:         "v1",
					cacheKeySeed: "seed1",
					value:        "result1",
					inputs: []solver.Edge{
						{Vertex: vtx(vtxOpt{
							name:         "v2",
							cacheKeySeed: "seed2",
							value:        "result2",
						})},
					},
					selectors: map[int]digest.Digest{
						0: dgst("sel0"),
					},
					slowCacheCompute: map[int]solver.ResultBasedCacheFunc{
						0: digestFromResult,
					},
				}),
			}},
		}),
	}
	g0.Vertex.(*vertex).setupCallCounters()

	res, err := j0.Build(ctx, g0)
	require.NoError(t, err)
	require.Equal(t, "result0", unwrap(res))

	require.Equal(t, int64(0), cacheManager.loadCounter)

	require.NoError(t, j0.Discard())
	j0 = nil

	j1, err := l.NewJob("j1")
	require.NoError(t, err)

	g0.Vertex.(*vertex).setupCallCounters()

	defer func() {
		if j1 != nil {
			j1.Discard()
		}
	}()

	res, err = j1.Build(ctx, g0)
	require.NoError(t, err)
	require.Equal(t, "result0", unwrap(res))

	require.NoError(t, j1.Discard())
	j1 = nil

	require.Equal(t, int64(3), *g0.Vertex.(*vertex).cacheCallCount)
	require.Equal(t, int64(0), *g0.Vertex.(*vertex).execCallCount)
	require.Equal(t, int64(1), cacheManager.loadCounter)
}

// testSlowCacheAvoidLoadOnCache tests a regression where an input with
// possible matches and a content based checksum should not try to checksum
// before other inputs with no keys have at least made into a slow state.
// moby/buildkit#648
func testSlowCacheAvoidLoadOnCache(t *testing.T, opts SolverTestOptions) {
	t.Parallel()
	ctx := t.Context()

	cacheManager := newTrackingCacheManager(opts.NewCacheManager(t))

	l := solver.NewSolver(solver.SolverOpt{
		ResolveOpFunc: testOpResolver,
		DefaultCache:  cacheManager,
	})
	defer l.Close()

	j0, err := l.NewJob("j0")
	require.NoError(t, err)

	defer func() {
		if j0 != nil {
			j0.Discard()
		}
	}()

	g0 := solver.Edge{
		Vertex: vtx(vtxOpt{
			name:         "vmain",
			cacheKeySeed: "seedmain",
			value:        "resultmain",
			inputs: []solver.Edge{{
				Vertex: vtx(vtxOpt{
					name:         "v0",
					cacheKeySeed: "seed0",
					value:        "result0",
					inputs: []solver.Edge{
						{
							Vertex: vtx(vtxOpt{
								name:         "v1",
								cacheKeySeed: "seed1",
								value:        "result1",
								inputs: []solver.Edge{{
									Vertex: vtx(vtxOpt{
										name:         "v3",
										cacheKeySeed: "seed3",
										value:        "result3",
									}),
								}},
								slowCacheCompute: map[int]solver.ResultBasedCacheFunc{
									0: digestFromResult,
								},
							}),
						},
						{
							Vertex: vtx(vtxOpt{
								name:         "v2",
								cacheKeySeed: "seed2",
								value:        "result2",
							}),
						},
					},
					slowCacheCompute: map[int]solver.ResultBasedCacheFunc{
						1: digestFromResult,
					},
				}),
			}},
		}),
	}
	g0.Vertex.(*vertex).setupCallCounters()

	res, err := j0.Build(ctx, g0)
	require.NoError(t, err)
	require.Equal(t, "resultmain", unwrap(res))

	require.Equal(t, int64(0), cacheManager.loadCounter)

	require.NoError(t, j0.Discard())
	j0 = nil

	j1, err := l.NewJob("j1")
	require.NoError(t, err)

	// the switch of the cache key for v3 forcing it to be reexecuted
	// testing that this does not cause v2 to be reloaded for cache for its
	// checksum recalculation
	g0 = solver.Edge{
		Vertex: vtx(vtxOpt{
			name:         "vmain",
			cacheKeySeed: "seedmain",
			value:        "resultmain",
			inputs: []solver.Edge{{
				Vertex: vtx(vtxOpt{
					name:         "v0",
					cacheKeySeed: "seed0",
					value:        "result0",
					inputs: []solver.Edge{
						{
							Vertex: vtx(vtxOpt{
								name:         "v1",
								cacheKeySeed: "seed1",
								value:        "result1",
								inputs: []solver.Edge{{
									Vertex: vtx(vtxOpt{
										name:         "v3",
										cacheKeySeed: "seed3-new",
										value:        "result3",
									}),
								}},
								execPreFunc: func(context.Context) error {
									select {
									case <-time.After(50 * time.Millisecond):
									case <-ctx.Done():
									}
									return nil
								},
								slowCacheCompute: map[int]solver.ResultBasedCacheFunc{
									0: digestFromResult,
								},
							}),
						},
						{
							Vertex: vtx(vtxOpt{
								name:         "v2",
								cacheKeySeed: "seed2",
								value:        "result2",
							}),
						},
					},
					slowCacheCompute: map[int]solver.ResultBasedCacheFunc{
						1: digestFromResult,
					},
				}),
			}},
		}),
	}

	g0.Vertex.(*vertex).setupCallCounters()

	defer func() {
		if j1 != nil {
			j1.Discard()
		}
	}()

	res, err = j1.Build(ctx, g0)
	require.NoError(t, err)
	require.Equal(t, "resultmain", unwrap(res))

	require.NoError(t, j1.Discard())
	j1 = nil

	require.Equal(t, int64(5), *g0.Vertex.(*vertex).cacheCallCount)
	require.Equal(t, int64(1), *g0.Vertex.(*vertex).execCallCount)
	require.Equal(t, int64(1), cacheManager.loadCounter)
}

func testCacheMultipleMaps(t *testing.T, opts SolverTestOptions) {
	t.Parallel()
	ctx := t.Context()

	cacheManager := newTrackingCacheManager(opts.NewCacheManager(t))

	l := solver.NewSolver(solver.SolverOpt{
		ResolveOpFunc: testOpResolver,
		DefaultCache:  cacheManager,
	})
	defer l.Close()

	j0, err := l.NewJob("j0")
	require.NoError(t, err)

	defer func() {
		if j0 != nil {
			j0.Discard()
		}
	}()

	g0 := solver.Edge{
		Vertex: vtx(vtxOpt{
			name:         "v0",
			cacheKeySeed: "seed0",
			cacheKeySeeds: []func() string{
				func() string { return "seed1" },
				func() string { return "seed2" },
			},
			value: "result0",
		}),
	}
	res, err := j0.Build(ctx, g0)
	require.NoError(t, err)
	require.Equal(t, "result0", unwrap(res))

	require.NoError(t, j0.Discard())
	j0 = nil

	expTarget := newTestExporterTarget()

	_, err = res.CacheKeys()[0].Exporter.ExportTo(ctx, expTarget, testExporterOpts(true))
	require.NoError(t, err)

	require.Equal(t, 3, expTarget.numUniqueRecords())

	j1, err := l.NewJob("j1")
	require.NoError(t, err)

	defer func() {
		if j1 != nil {
			j1.Discard()
		}
	}()

	called := false
	g1 := solver.Edge{
		Vertex: vtx(vtxOpt{
			name:         "v1",
			cacheKeySeed: "seed1",
			cacheKeySeeds: []func() string{
				func() string { called = true; return "seed3" },
			},
			value: "result0-not-cached",
		}),
	}

	res, err = j1.Build(ctx, g1)
	require.NoError(t, err)
	require.Equal(t, "result0", unwrap(res))

	require.NoError(t, j1.Discard())
	j1 = nil

	expTarget = newTestExporterTarget()

	_, err = res.CacheKeys()[0].Exporter.ExportTo(ctx, expTarget, testExporterOpts(true))
	require.NoError(t, err)

	require.Equal(t, 3, expTarget.numUniqueRecords())
	require.Equal(t, false, called)

	j2, err := l.NewJob("j2")
	require.NoError(t, err)

	defer func() {
		if j2 != nil {
			j2.Discard()
		}
	}()

	g2 := solver.Edge{
		Vertex: vtx(vtxOpt{
			name:         "v2",
			cacheKeySeed: "seed3",
			cacheKeySeeds: []func() string{
				func() string { called = true; return "seed2" },
			},
			value: "result0-not-cached",
		}),
	}

	res, err = j2.Build(ctx, g2)
	require.NoError(t, err)
	require.Equal(t, "result0", unwrap(res))

	require.NoError(t, j2.Discard())
	j2 = nil

	expTarget = newTestExporterTarget()

	_, err = res.CacheKeys()[0].Exporter.ExportTo(ctx, expTarget, testExporterOpts(true))
	require.NoError(t, err)

	require.Equal(t, 3, expTarget.numUniqueRecords())
	require.Equal(t, true, called)
}

func testCacheInputMultipleMaps(t *testing.T, opts SolverTestOptions) {
	t.Parallel()
	ctx := t.Context()

	cacheManager := newTrackingCacheManager(opts.NewCacheManager(t))

	l := solver.NewSolver(solver.SolverOpt{
		ResolveOpFunc: testOpResolver,
		DefaultCache:  cacheManager,
	})
	defer l.Close()

	j0, err := l.NewJob("j0")
	require.NoError(t, err)

	defer func() {
		if j0 != nil {
			j0.Discard()
		}
	}()

	g0 := solver.Edge{
		Vertex: vtx(vtxOpt{
			name:         "v0",
			cacheKeySeed: "seed0",
			value:        "result0",
			inputs: []solver.Edge{{
				Vertex: vtx(vtxOpt{
					name:         "v1",
					cacheKeySeed: "seed1",
					cacheKeySeeds: []func() string{
						func() string { return "seed2" },
					},
					value: "result1",
				}),
			}},
		}),
	}
	res, err := j0.Build(ctx, g0)
	require.NoError(t, err)
	require.Equal(t, "result0", unwrap(res))

	expTarget := newTestExporterTarget()

	_, err = res.CacheKeys()[0].Exporter.ExportTo(ctx, expTarget, testExporterOpts(true))
	require.NoError(t, err)

	require.Equal(t, 3, expTarget.numUniqueRecords())

	require.NoError(t, j0.Discard())
	j0 = nil

	j1, err := l.NewJob("j1")
	require.NoError(t, err)

	defer func() {
		if j1 != nil {
			j1.Discard()
		}
	}()

	g1 := solver.Edge{
		Vertex: vtx(vtxOpt{
			name:         "v0",
			cacheKeySeed: "seed0",
			value:        "result0-no-cache",
			inputs: []solver.Edge{{
				Vertex: vtx(vtxOpt{
					name:         "v1",
					cacheKeySeed: "seed1.changed",
					cacheKeySeeds: []func() string{
						func() string { return "seed2" },
					},
					value: "result1-no-cache",
				}),
			}},
		}),
	}
	res, err = j1.Build(ctx, g1)
	require.NoError(t, err)
	require.Equal(t, "result0", unwrap(res))

	_, err = res.CacheKeys()[0].Exporter.ExportTo(ctx, expTarget, testExporterOpts(true))
	require.NoError(t, err)

	require.Equal(t, 3, expTarget.numUniqueRecords())

	require.NoError(t, j1.Discard())
	j1 = nil
}

func testCacheExportingPartialSelector(t *testing.T, opts SolverTestOptions) {
	t.Skip("todo")
	t.Parallel()
	ctx := t.Context()

	cacheManager := newTrackingCacheManager(opts.NewCacheManager(t))

	l := solver.NewSolver(solver.SolverOpt{
		ResolveOpFunc: testOpResolver,
		DefaultCache:  cacheManager,
	})
	defer l.Close()

	j0, err := l.NewJob("j0")
	require.NoError(t, err)

	defer func() {
		if j0 != nil {
			j0.Discard()
		}
	}()

	g0 := solver.Edge{
		Vertex: vtx(vtxOpt{
			name:         "v0",
			cacheKeySeed: "seed0",
			value:        "result0",
			inputs: []solver.Edge{
				{Vertex: vtx(vtxOpt{
					name:         "v1",
					cacheKeySeed: "seed1",
					value:        "result1",
				})},
			},
			selectors: map[int]digest.Digest{
				0: dgst("sel0"),
			},
			slowCacheCompute: map[int]solver.ResultBasedCacheFunc{
				0: digestFromResult,
			},
		}),
	}

	res, err := j0.Build(ctx, g0)
	require.NoError(t, err)
	require.Equal(t, "result0", unwrap(res))

	require.NoError(t, j0.Discard())
	j0 = nil

	expTarget := newTestExporterTarget()

	_, err = res.CacheKeys()[0].Exporter.ExportTo(ctx, expTarget, testExporterOpts(true))
	require.NoError(t, err)

	require.Equal(t, 3, expTarget.numUniqueRecords())
	require.Equal(t, 0, expTarget.records[0].results)
	require.Equal(t, 0, expTarget.records[1].results)
	require.Equal(t, 0, expTarget.records[2].results)
	require.Equal(t, 1, expTarget.records[5].results)
	require.Equal(t, 0, expTarget.records[0].links)
	require.Equal(t, 0, expTarget.records[1].links)
	require.Equal(t, 0, expTarget.records[2].links)
	require.Equal(t, 1, expTarget.records[5].links)

	// repeat so that all coming from cache are retained
	j1, err := l.NewJob("j1")
	require.NoError(t, err)

	defer func() {
		if j1 != nil {
			j1.Discard()
		}
	}()

	g1 := g0

	res, err = j1.Build(ctx, g1)
	require.NoError(t, err)
	require.Equal(t, "result0", unwrap(res))

	require.NoError(t, j1.Discard())
	j1 = nil

	expTarget = newTestExporterTarget()

	_, err = res.CacheKeys()[0].Exporter.ExportTo(ctx, expTarget, testExporterOpts(true))
	require.NoError(t, err)

	// the order of the records isn't really significant
	require.Equal(t, 3, expTarget.numUniqueRecords())
	require.Equal(t, 0, expTarget.records[0].results)
	require.Equal(t, 0, expTarget.records[1].results)
	require.Equal(t, 1, expTarget.records[4].results)
	require.Equal(t, 0, expTarget.records[0].links)
	require.Equal(t, 0, expTarget.records[1].links)
	require.Equal(t, 1, expTarget.records[4].links)

	// repeat with forcing a slow key recomputation
	j2, err := l.NewJob("j2")
	require.NoError(t, err)

	defer func() {
		if j2 != nil {
			j2.Discard()
		}
	}()

	g2 := solver.Edge{
		Vertex: vtx(vtxOpt{
			name:         "v0",
			cacheKeySeed: "seed0",
			value:        "result0",
			inputs: []solver.Edge{
				{Vertex: vtx(vtxOpt{
					name:         "v1",
					cacheKeySeed: "seed1-net",
					value:        "result1",
				})},
			},
			selectors: map[int]digest.Digest{
				0: dgst("sel0"),
			},
			slowCacheCompute: map[int]solver.ResultBasedCacheFunc{
				0: digestFromResult,
			},
		}),
	}

	res, err = j2.Build(ctx, g2)
	require.NoError(t, err)
	require.Equal(t, "result0", unwrap(res))

	require.NoError(t, j2.Discard())
	j2 = nil

	expTarget = newTestExporterTarget()

	_, err = res.CacheKeys()[0].Exporter.ExportTo(ctx, expTarget, testExporterOpts(true))
	require.NoError(t, err)

	// the order of the records isn't really significant
	// adds one
	require.Equal(t, 4, expTarget.numUniqueRecords())
	require.Equal(t, 0, expTarget.records[0].results)
	require.Equal(t, 0, expTarget.records[1].results)
	require.Equal(t, 1, expTarget.records[6].results)
	require.Equal(t, 0, expTarget.records[0].links)
	require.Equal(t, 0, expTarget.records[1].links)
	require.Equal(t, 0, expTarget.records[2].links)
	require.Equal(t, 1, expTarget.records[6].links)

	// repeat with a wrapper
	j3, err := l.NewJob("j3")
	require.NoError(t, err)

	defer func() {
		if j3 != nil {
			j3.Discard()
		}
	}()

	g3 := solver.Edge{
		Vertex: vtx(vtxOpt{
			name:         "v2",
			cacheKeySeed: "seed2",
			value:        "result2",
			inputs:       []solver.Edge{g2},
		},
		),
	}

	res, err = j3.Build(ctx, g3)
	require.NoError(t, err)
	require.Equal(t, "result2", unwrap(res))

	require.NoError(t, j3.Discard())
	j3 = nil

	expTarget = newTestExporterTarget()

	_, err = res.CacheKeys()[0].Exporter.ExportTo(ctx, expTarget, testExporterOpts(true))
	require.NoError(t, err)

	// adds one extra result
	// the order of the records isn't really significant
	require.Equal(t, 5, expTarget.numUniqueRecords())
	require.Equal(t, 0, expTarget.records[0].results)
	require.Equal(t, 0, expTarget.records[1].results)
	require.Equal(t, 0, expTarget.records[2].results)
	require.Equal(t, 1, expTarget.records[5].results)
	require.Equal(t, 1, expTarget.records[7].results)
	require.Equal(t, 0, expTarget.records[0].links)
	require.Equal(t, 0, expTarget.records[1].links)
	require.Equal(t, 0, expTarget.records[2].links)
	require.Equal(t, 1, expTarget.records[5].links)
	require.Equal(t, 1, expTarget.records[7].links)
}

func testCacheExportingMergedKey(t *testing.T, opts SolverTestOptions) {
	t.Parallel()
	ctx := t.Context()

	cacheManager := newTrackingCacheManager(opts.NewCacheManager(t))

	l := solver.NewSolver(solver.SolverOpt{
		ResolveOpFunc: testOpResolver,
		DefaultCache:  cacheManager,
	})
	defer l.Close()

	j0, err := l.NewJob("j0")
	require.NoError(t, err)

	defer func() {
		if j0 != nil {
			j0.Discard()
		}
	}()

	g0 := solver.Edge{
		Vertex: vtx(vtxOpt{
			name:         "v0",
			cacheKeySeed: "seed0",
			value:        "result0",
			inputs: []solver.Edge{
				{
					Vertex: vtx(vtxOpt{
						name:         "v1",
						cacheKeySeed: "seed1",
						value:        "result1",
						inputs: []solver.Edge{
							{
								Vertex: vtx(vtxOpt{
									name:         "v2",
									cacheKeySeed: "seed2",
									value:        "result2",
								}),
							},
						},
						slowCacheCompute: map[int]solver.ResultBasedCacheFunc{
							0: digestFromResult,
						},
					}),
				},
				{
					Vertex: vtx(vtxOpt{
						name:         "v1-diff",
						cacheKeySeed: "seed1",
						value:        "result1",
						inputs: []solver.Edge{
							{
								Vertex: vtx(vtxOpt{
									name:         "v3",
									cacheKeySeed: "seed3",
									value:        "result2",
								}),
							},
						},
						slowCacheCompute: map[int]solver.ResultBasedCacheFunc{
							0: digestFromResult,
						},
					}),
				},
			},
		}),
	}

	res, err := j0.Build(ctx, g0)
	require.NoError(t, err)
	require.Equal(t, "result0", unwrap(res))

	require.NoError(t, j0.Discard())
	j0 = nil

	expTarget := newTestExporterTarget()

	_, err = res.CacheKeys()[0].Exporter.ExportTo(ctx, expTarget, testExporterOpts(true))
	require.NoError(t, err)

	require.Equal(t, 5, expTarget.numUniqueRecords())
}

// moby/buildkit#434
func testMergedEdgesLookup(t *testing.T, opts SolverTestOptions) {
	t.Parallel()

	// this test requires multiple runs to trigger the race
	for range 20 {
		func() {
			ctx := t.Context()

			cacheManager := newTrackingCacheManager(opts.NewCacheManager(t))

			l := solver.NewSolver(solver.SolverOpt{
				ResolveOpFunc: testOpResolver,
				DefaultCache:  cacheManager,
			})
			defer l.Close()

			j0, err := l.NewJob("j0")
			require.NoError(t, err)

			defer func() {
				if j0 != nil {
					j0.Discard()
				}
			}()

			g := solver.Edge{
				Vertex: vtxSum(3, vtxOpt{inputs: []solver.Edge{
					{Vertex: vtxSum(0, vtxOpt{inputs: []solver.Edge{
						{Vertex: vtxSum(2, vtxOpt{inputs: []solver.Edge{
							{Vertex: vtxConst(2, vtxOpt{})},
						}})},
						{Vertex: vtxConst(0, vtxOpt{})},
					}})},
					{Vertex: vtxSum(2, vtxOpt{inputs: []solver.Edge{
						{Vertex: vtxConst(2, vtxOpt{})},
					}})},
				}}),
			}
			g.Vertex.(*vertexSum).setupCallCounters()

			res, err := j0.Build(ctx, g)
			require.NoError(t, err)
			require.Equal(t, 11, unwrapInt(res))

			require.Equal(t, int64(7), *g.Vertex.(*vertexSum).cacheCallCount)
			require.Equal(t, int64(0), cacheManager.loadCounter)

			require.NoError(t, j0.Discard())
			j0 = nil
		}()
	}
}

func testMergedEdgesCycle(t *testing.T, opts SolverTestOptions) {
	t.Parallel()

	for range 20 {
		ctx := t.Context()

		cacheManager := newTrackingCacheManager(opts.NewCacheManager(t))

		l := solver.NewSolver(solver.SolverOpt{
			ResolveOpFunc: testOpResolver,
			DefaultCache:  cacheManager,
		})
		defer l.Close()

		j0, err := l.NewJob("j0")
		require.NoError(t, err)

		defer func() {
			if j0 != nil {
				j0.Discard()
			}
		}()

		// 2 different vertices, va and vb, both with the same cache key
		va := vtxAdd(2, vtxOpt{name: "va", inputs: []solver.Edge{
			{Vertex: vtxConst(3, vtxOpt{})},
			{Vertex: vtxConst(4, vtxOpt{})},
		}})
		vb := vtxAdd(2, vtxOpt{name: "vb", inputs: []solver.Edge{
			{Vertex: vtxConst(3, vtxOpt{})},
			{Vertex: vtxConst(4, vtxOpt{})},
		}})

		// 4 edges va[0], va[1], vb[0], vb[1]
		// by ordering them like this, we try and trigger merge va[0]->vb[0] and
		// vb[1]->va[1] to cause a cycle
		g := solver.Edge{
			Vertex: vtxSum(1, vtxOpt{inputs: []solver.Edge{
				{Vertex: va, Index: 1}, // 6
				{Vertex: vb, Index: 0}, // 5
				{Vertex: va, Index: 0}, // 5
				{Vertex: vb, Index: 1}, // 6
			}}),
		}
		g.Vertex.(*vertexSum).setupCallCounters()

		res, err := j0.Build(ctx, g)
		require.NoError(t, err)
		require.Equal(t, 23, unwrapInt(res))

		require.NoError(t, j0.Discard())
		j0 = nil
	}
}

func testMergedEdgesCycleMultipleOwners(t *testing.T, opts SolverTestOptions) {
	t.Parallel()

	for range 20 {
		ctx := t.Context()

		cacheManager := newTrackingCacheManager(opts.NewCacheManager(t))

		l := solver.NewSolver(solver.SolverOpt{
			ResolveOpFunc: testOpResolver,
			DefaultCache:  cacheManager,
		})
		defer l.Close()

		j0, err := l.NewJob("j0")
		require.NoError(t, err)

		defer func() {
			if j0 != nil {
				j0.Discard()
			}
		}()

		va := vtxAdd(2, vtxOpt{name: "va", inputs: []solver.Edge{
			{Vertex: vtxConst(3, vtxOpt{})},
			{Vertex: vtxConst(4, vtxOpt{})},
			{Vertex: vtxConst(5, vtxOpt{})},
		}})
		vb := vtxAdd(2, vtxOpt{name: "vb", inputs: []solver.Edge{
			{Vertex: vtxConst(3, vtxOpt{})},
			{Vertex: vtxConst(4, vtxOpt{})},
			{Vertex: vtxConst(5, vtxOpt{})},
		}})
		vc := vtxAdd(2, vtxOpt{name: "vc", inputs: []solver.Edge{
			{Vertex: vtxConst(3, vtxOpt{})},
			{Vertex: vtxConst(4, vtxOpt{})},
			{Vertex: vtxConst(5, vtxOpt{})},
		}})

		g := solver.Edge{
			Vertex: vtxSum(1, vtxOpt{inputs: []solver.Edge{
				// we trigger merge va[0]->vb[0] and va[1]->vc[1] so that va gets
				// been merged twice
				{Vertex: vb, Index: 0}, // 5
				{Vertex: va, Index: 0}, // 5

				{Vertex: vc, Index: 1}, // 6
				{Vertex: va, Index: 1}, // 6

				// then we trigger another merge via the first owner vb[1]->va[1]
				// that must be flipped
				{Vertex: va, Index: 2}, // 7
				{Vertex: vb, Index: 2}, // 7
			}}),
		}
		g.Vertex.(*vertexSum).setupCallCounters()

		res, err := j0.Build(ctx, g)
		require.NoError(t, err)
		require.Equal(t, 37, unwrapInt(res))

		require.NoError(t, j0.Discard())
		j0 = nil
	}
}

func testUnknownBuildID(t *testing.T, opts SolverTestOptions) {
	s := solver.NewSolver(solver.SolverOpt{
		ResolveOpFunc: testOpResolver,
		DefaultCache:  opts.NewCacheManager(t),
	})
	defer s.Close()

	_, err := s.Get(identity.NewID())
	require.Error(t, err)
	require.Contains(t, err.Error(), "no such job")
}

func testCacheErrNotFound(t *testing.T, opts SolverTestOptions) {
	t.Skip("todo")
}

func testCacheLoadError(t *testing.T, opts SolverTestOptions) {
	t.Parallel()

	ctx := t.Context()

	cacheManager := newTrackingCacheManager(opts.NewCacheManager(t))

	l := solver.NewSolver(solver.SolverOpt{
		ResolveOpFunc: testOpResolver,
		DefaultCache:  cacheManager,
	})
	defer l.Close()

	j0, err := l.NewJob("j0")
	require.NoError(t, err)

	defer func() {
		if j0 != nil {
			j0.Discard()
		}
	}()

	g := solver.Edge{
		Vertex: vtxSum(3, vtxOpt{inputs: []solver.Edge{
			{Vertex: vtxSum(0, vtxOpt{inputs: []solver.Edge{
				{Vertex: vtxSum(2, vtxOpt{inputs: []solver.Edge{
					{Vertex: vtxConst(2, vtxOpt{})},
				}})},
				{Vertex: vtxConst(0, vtxOpt{})},
			}})},
			{Vertex: vtxSum(2, vtxOpt{inputs: []solver.Edge{
				{Vertex: vtxConst(2, vtxOpt{})},
			}})},
		}}),
	}
	g.Vertex.(*vertexSum).setupCallCounters()

	res, err := j0.Build(ctx, g)
	require.NoError(t, err)
	require.Equal(t, 11, unwrapInt(res))
	require.Equal(t, int64(7), *g.Vertex.(*vertexSum).cacheCallCount)
	require.Equal(t, int64(5), *g.Vertex.(*vertexSum).execCallCount)
	require.Equal(t, int64(0), cacheManager.loadCounter)

	require.NoError(t, j0.Discard())
	j0 = nil

	// repeat with cache
	j1, err := l.NewJob("j1")
	require.NoError(t, err)

	defer func() {
		if j1 != nil {
			j1.Discard()
		}
	}()

	g1 := g

	g1.Vertex.(*vertexSum).setupCallCounters()

	res, err = j1.Build(ctx, g1)
	require.NoError(t, err)
	require.Equal(t, 11, unwrapInt(res))
	require.Equal(t, int64(7), *g.Vertex.(*vertexSum).cacheCallCount)
	require.Equal(t, int64(0), *g.Vertex.(*vertexSum).execCallCount)
	require.Equal(t, int64(1), cacheManager.loadCounter)

	require.NoError(t, j1.Discard())
	j1 = nil

	// repeat with cache but loading will now fail
	j2, err := l.NewJob("j2")
	require.NoError(t, err)

	defer func() {
		if j2 != nil {
			j2.Discard()
		}
	}()

	g2 := g

	g2.Vertex.(*vertexSum).setupCallCounters()

	cacheManager.forceFail = true

	res, err = j2.Build(ctx, g2)
	require.NoError(t, err)
	require.Equal(t, 11, unwrapInt(res))
	require.Equal(t, int64(7), *g.Vertex.(*vertexSum).cacheCallCount)
	require.Equal(t, int64(5), *g.Vertex.(*vertexSum).execCallCount)
	require.Equal(t, int64(6), cacheManager.loadCounter)

	require.NoError(t, j2.Discard())
	j2 = nil
}

func testInputRequestDeadlock(t *testing.T, opts SolverTestOptions) {
	t.Parallel()
	ctx := t.Context()

	s := solver.NewSolver(solver.SolverOpt{
		ResolveOpFunc: testOpResolver,
		DefaultCache:  opts.NewCacheManager(t),
	})
	defer s.Close()

	j0, err := s.NewJob("job0")
	require.NoError(t, err)

	defer func() {
		if j0 != nil {
			j0.Discard()
		}
	}()

	g0 := solver.Edge{
		Vertex: vtx(vtxOpt{
			name:         "v0",
			cacheKeySeed: "seed0",
			value:        "result0",
			inputs: []solver.Edge{
				{
					Vertex: vtx(vtxOpt{
						name:         "v1",
						cacheKeySeed: "seed1",
						value:        "result1",
					}),
				},
				{
					Vertex: vtx(vtxOpt{
						name:         "v2",
						cacheKeySeed: "seed2",
						value:        "result2",
					}),
				},
			},
		}),
	}

	_, err = j0.Build(ctx, g0)
	require.NoError(t, err)
	require.NoError(t, j0.Discard())
	j0 = nil

	j1, err := s.NewJob("job1")
	require.NoError(t, err)

	defer func() {
		if j1 != nil {
			j1.Discard()
		}
	}()

	g1 := solver.Edge{
		Vertex: vtx(vtxOpt{
			name:         "v0",
			cacheKeySeed: "seed0-1",
			value:        "result0",
			inputs: []solver.Edge{
				{
					Vertex: vtx(vtxOpt{
						name:         "v1",
						cacheKeySeed: "seed1-1",
						value:        "result1",
					}),
				},
				{
					Vertex: vtx(vtxOpt{
						name:         "v2",
						cacheKeySeed: "seed2-1",
						value:        "result2",
					}),
				},
			},
		}),
	}

	_, err = j1.Build(ctx, g1)
	require.NoError(t, err)
	require.NoError(t, j1.Discard())
	j1 = nil

	j2, err := s.NewJob("job2")
	require.NoError(t, err)

	defer func() {
		if j2 != nil {
			j2.Discard()
		}
	}()

	g2 := solver.Edge{
		Vertex: vtx(vtxOpt{
			name:         "v0",
			cacheKeySeed: "seed0-1",
			value:        "result0",
			inputs: []solver.Edge{
				{
					Vertex: vtx(vtxOpt{
						name:         "v1",
						cacheKeySeed: "seed1",
						value:        "result1",
					}),
				},
				{
					Vertex: vtx(vtxOpt{
						name:         "v2",
						cacheKeySeed: "seed2-1",
						value:        "result2",
					}),
				},
			},
			slowCacheCompute: map[int]solver.ResultBasedCacheFunc{
				1: digestFromResult,
			},
		}),
	}

	_, err = j2.Build(ctx, g2)
	require.NoError(t, err)
	require.NoError(t, j2.Discard())
	j2 = nil
}

func testCacheMapDependencyCount(t *testing.T, opts SolverTestOptions) {
	t.Parallel()

	for _, depCount := range []int{0, 2} {
		t.Run(fmt.Sprintf("deps=%d", depCount), func(t *testing.T) {
			s := solver.NewSolver(solver.SolverOpt{
				ResolveOpFunc: testOpResolver,
				DefaultCache:  opts.NewCacheManager(t),
			})
			defer s.Close()

			job, err := s.NewJob(fmt.Sprintf("invalid-cache-map-%d", depCount))
			require.NoError(t, err)
			defer job.Discard()

			v := &invalidCacheMapVertex{
				vertex: vtx(vtxOpt{
					name: fmt.Sprintf("invalid-cache-map-%d", depCount),
					inputs: []solver.Edge{{
						Vertex: vtx(vtxOpt{name: "input"}),
					}},
				}),
				depCount: depCount,
			}
			_, err = job.Build(t.Context(), solver.Edge{Vertex: v})
			require.ErrorContains(t, err, fmt.Sprintf("invalid cache map: expected dependency count 1, got %d", depCount))
		})
	}
}

func testStaleEdgeMerge(t *testing.T, opts SolverTestOptions) {
	// should not be possible to merge to an edge no longer in the actives map
	t.Parallel()
	ctx := t.Context()

	s := solver.NewSolver(solver.SolverOpt{
		ResolveOpFunc: testOpResolver,
		DefaultCache:  opts.NewCacheManager(t),
	})
	defer s.Close()

	depV0 := vtxConst(1, vtxOpt{name: "depV0"})
	depV1 := vtxConst(1, vtxOpt{name: "depV1"})
	depV2 := vtxConst(1, vtxOpt{name: "depV2"})

	// These should all end up edge merged
	v0 := vtxAdd(2, vtxOpt{name: "v0", inputs: []solver.Edge{
		{Vertex: depV0},
	}})
	v1 := vtxAdd(2, vtxOpt{name: "v1", inputs: []solver.Edge{
		{Vertex: depV1},
	}})
	v2 := vtxAdd(2, vtxOpt{name: "v2", inputs: []solver.Edge{
		{Vertex: depV2},
	}})

	j0, err := s.NewJob("job0")
	require.NoError(t, err)
	g0 := solver.Edge{Vertex: v0}
	res, err := j0.Build(ctx, g0)
	require.NoError(t, err)
	require.NotNil(t, res)

	actives := s.Inspect()
	require.Contains(t, actives, v0.Digest())
	require.True(t, contains(actives[v0.Digest()], j0))
	require.Contains(t, actives, depV0.Digest())
	require.True(t, contains(actives[depV0.Digest()], j0))

	// this edge should be merged with the one from j0
	j1, err := s.NewJob("job1")
	require.NoError(t, err)
	g1 := solver.Edge{Vertex: v1}
	res, err = j1.Build(ctx, g1)
	require.NoError(t, err)
	require.NotNil(t, res)

	actives = s.Inspect()
	require.Contains(t, actives, v0.Digest())
	require.True(t, contains(actives[v0.Digest()], j0))
	require.True(t, contains(actives[v0.Digest()], j1))
	require.Contains(t, actives, depV0.Digest())
	require.True(t, contains(actives[depV0.Digest()], j0))
	require.True(t, contains(actives[depV0.Digest()], j1))

	require.Contains(t, actives, v1.Digest())
	require.False(t, contains(actives[v1.Digest()], j0))
	require.True(t, contains(actives[v1.Digest()], j1))
	require.Contains(t, actives, depV1.Digest())
	require.False(t, contains(actives[depV1.Digest()], j0))
	require.True(t, contains(actives[depV1.Digest()], j1))

	// discard j0, verify that v0 is still active and it's state contains j1 since j1's
	// edge was merged to v0's state
	require.NoError(t, j0.Discard())

	actives = s.Inspect()
	require.Contains(t, actives, v0.Digest())
	require.False(t, contains(actives[v0.Digest()], j0))
	require.True(t, contains(actives[v0.Digest()], j1))
	require.Contains(t, actives, depV0.Digest())
	require.False(t, contains(actives[depV0.Digest()], j0))
	require.True(t, contains(actives[depV0.Digest()], j1))

	require.Contains(t, actives, v1.Digest())
	require.False(t, contains(actives[v1.Digest()], j0))
	require.True(t, contains(actives[v1.Digest()], j1))
	require.Contains(t, actives, depV1.Digest())
	require.False(t, contains(actives[depV1.Digest()], j0))
	require.True(t, contains(actives[depV1.Digest()], j1))

	// verify another job can still merge
	j2, err := s.NewJob("job2")
	require.NoError(t, err)
	g2 := solver.Edge{Vertex: v2}
	res, err = j2.Build(ctx, g2)
	require.NoError(t, err)
	require.NotNil(t, res)

	actives = s.Inspect()
	require.Contains(t, actives, v0.Digest())
	require.True(t, contains(actives[v0.Digest()], j1))
	require.True(t, contains(actives[v0.Digest()], j2))
	require.Contains(t, actives, depV0.Digest())
	require.True(t, contains(actives[depV0.Digest()], j1))
	require.True(t, contains(actives[depV0.Digest()], j2))

	require.Contains(t, actives, v1.Digest())
	require.True(t, contains(actives[v1.Digest()], j1))
	require.False(t, contains(actives[v1.Digest()], j2))
	require.Contains(t, actives, depV1.Digest())
	require.True(t, contains(actives[depV1.Digest()], j1))
	require.False(t, contains(actives[depV1.Digest()], j2))

	require.Contains(t, actives, v2.Digest())
	require.False(t, contains(actives[v2.Digest()], j1))
	require.True(t, contains(actives[v2.Digest()], j2))
	require.Contains(t, actives, depV2.Digest())
	require.False(t, contains(actives[depV2.Digest()], j1))
	require.True(t, contains(actives[depV2.Digest()], j2))

	// discard j1, verify only referenced edges still exist
	require.NoError(t, j1.Discard())

	actives = s.Inspect()
	require.Contains(t, actives, v0.Digest())
	require.False(t, contains(actives[v0.Digest()], j1))
	require.True(t, contains(actives[v0.Digest()], j2))
	require.Contains(t, actives, depV0.Digest())
	require.False(t, contains(actives[depV0.Digest()], j1))
	require.True(t, contains(actives[depV0.Digest()], j2))

	require.NotContains(t, actives, v1.Digest())
	require.NotContains(t, actives, depV1.Digest())

	require.Contains(t, actives, v2.Digest())
	require.True(t, contains(actives[v2.Digest()], j2))
	require.Contains(t, actives, depV2.Digest())
	require.True(t, contains(actives[depV2.Digest()], j2))

	// discard the last job and verify everything was removed now
	require.NoError(t, j2.Discard())
	actives = s.Inspect()
	require.NotContains(t, actives, v0.Digest())
	require.NotContains(t, actives, v1.Digest())
	require.NotContains(t, actives, v2.Digest())
	require.NotContains(t, actives, depV0.Digest())
	require.NotContains(t, actives, depV1.Digest())
	require.NotContains(t, actives, depV2.Digest())
}

// testCacheMissStaleCompleteSharedDep reproduces the "stale complete
// shared-dependency" cache miss (moby/buildkit#4674 family) in-process.
//
// Graph (identical for every build): a shared leaf "base" and one op "step" on
// top of it (arrow = "is an input of"):
//
//	"base" --> "step"
//
// A1 builds step -> base against an empty cache. base executes; on unfixed
// code its cache key then lived only on base's *result*, never on
// edgeState.keys (a leaf that executed against an empty cache never appended a
// key, and a completed edge never re-queries; the fix appends the committed
// keys to edgeState.keys at completion). Both base and step are saved. A2 then
// builds a step with the SAME cache seed on the SAME shared base and should
// reuse the cached step.
//
// A parent learns a completed dep's key one of two ways: (a) probeCache/Query,
// fed only from the dep's edgeState.keys, or (b) an edge-index merge onto a
// still-*live* peer edge, which reads result.CacheKeys(). Discarding A1 removes
// its step edge from the edge index, closing (b) -- so A2's step edge must
// resolve through the *cache*, not a live peer. (The index is keyed on the cache
// key, not the vertex digest, so a distinct step digest alone would not close
// (b); the discard does.)
//
// Whether the shared base is reused-complete or recreated-fresh is then decided
// purely by concurrency:
//
//   - No B: after A1 is discarded the base is unreferenced and dropped from
//     Solver.actives. A2 recreates it fresh; it re-queries the populated cache
//     and delivers its key via edgeState.keys -> A2 probes -> hit.
//   - B holds only the base alive: the base stays referenced in Solver.actives,
//     already `complete`, and is reused by A2. On unfixed code it delivered
//     edgeState.keys=0 while its result carried the key -- probeCache saw an
//     empty slice, noCacheMatchPossible latched, and A2 re-executed; post-fix
//     the executed base appended its committed keys to edgeState.keys at
//     completion, so A2 receives them, probes, and hits.
func testCacheMissStaleCompleteSharedDep(t *testing.T, opt SolverTestOptions) {
	// baseEdge builds a leaf "base" vertex with a fixed digest and cache seed so
	// it is a single shared active vertex across builds. value differs per build
	// only to prove the returned result came from A1's cache. ignoreCache marks
	// the base as `--no-cache` (used by the ignore-cache safety subtest).
	baseEdge := func(value string, ignoreCache bool) solver.Edge {
		return solver.Edge{Vertex: vtx(vtxOpt{
			name:         "base",
			cacheKeySeed: "base-seed",
			value:        value,
			ignoreCache:  ignoreCache,
		})}
	}

	// stepEdge builds a "step" vertex on top of base. The cache seed is fixed so
	// every build's step is cache-matchable against A1's saved
	// record; stepName (hence the vertex digest) is passed in so a build can
	// either reuse A1's step vertex (same name) or model a separate
	// build with a distinct step vertex (different name, same seed). ignoreBase
	// marks the base dependency as `--no-cache`.
	stepEdge := func(stepName, value, baseValue string, ignoreBase bool) solver.Edge {
		return solver.Edge{Vertex: vtx(vtxOpt{
			name:         stepName,
			cacheKeySeed: "step-seed",
			value:        value,
			inputs:       []solver.Edge{baseEdge(baseValue, ignoreBase)},
		})}
	}

	// scenario selects which timing / builder-state case run() exercises.
	type scenario struct {
		holdBaseAlive  bool // B keeps the shared base referenced across A2 (concurrent hold)
		sameStepVertex bool // A2 reuses A1's step vertex digest vs. a distinct one
		warmBase       bool // base's cache record pre-exists, so it cache-hits instead of executing
		ignoreBase     bool // base carries IgnoreCache (`--no-cache` on the dependency)
	}
	run := func(t *testing.T, scenario scenario) (secondExecs int64, secondResult string) {
		ctx := t.Context()

		var jobB *solver.Job
		solver := solver.NewSolver(solver.SolverOpt{
			ResolveOpFunc: testOpResolver,
			DefaultCache:  opt.NewCacheManager(t),
		})
		defer solver.Close()

		// warmBase models a WARM builder (scenario 2b): busybox is already
		// pulled, so base's persistent-cache record already exists before
		// A1. A throwaway build of base alone (then discarded) Saves that
		// record. A1's base then cache-HITS its own query -- it does
		// NOT execute and leaves its key in edgeState.keys -- whereas on a cold
		// cache base EXECUTES, which on unfixed code left its key only on the
		// result. The warm-up's distinct value ("base-result-warmup") lets us
		// confirm A1's base came from the cache rather than executing.
		if scenario.warmBase {
			warmJob, err := solver.NewJob("warmup")
			require.NoError(t, err)
			_, err = warmJob.Build(ctx, baseEdge("base-result-warmup", scenario.ignoreBase))
			require.NoError(t, err)
			require.NoError(t, warmJob.Discard())
		}

		// A1: build step -> base. Cold cache: both execute and are saved (on
		// unfixed code base then held its key on its result only). Warm cache: base
		// cache-hits its own query (no exec), only step executes; base's key is in
		// edgeState.keys.
		jobA1, err := solver.NewJob("a1")
		require.NoError(t, err)

		edgeA1 := stepEdge("step", "step-result", "base-result", scenario.ignoreBase)
		edgeA1.Vertex.(*vertex).setupCallCounters()

		jobA1Result, err := jobA1.Build(ctx, edgeA1)
		require.NoError(t, err)
		require.Equal(t, "step-result", unwrap(jobA1Result))
		// Cold: A1 executed BOTH base and step against the empty
		// cache. Warm: base cache-hit the warm-up's record, so only step executed.
		wantA1Execs := int64(2)
		if scenario.warmBase {
			wantA1Execs = int64(1)
		}
		require.Equal(t, wantA1Execs, *edgeA1.Vertex.(*vertex).execCallCount)

		// Optionally, B keeps ONLY the shared base alive in
		// Solver.actives across A1's teardown (the unrelated build
		// that happens to share the base). It must attach to base before
		// A1 is discarded.
		if scenario.holdBaseAlive {
			jobB, err = solver.NewJob("b")
			require.NoError(t, err)
			jobBResult, err := jobB.Build(ctx, baseEdge("base-result-concurrent", scenario.ignoreBase))
			require.NoError(t, err)
			// base was reused from A1's completed active edge, so
			// B sees whatever value that base carries: the value
			// A1 execed (cold) or the warm-up record it cache-hit (warm).
			wantBaseValue := "base-result"
			if scenario.warmBase {
				wantBaseValue = "base-result-warmup"
			}
			require.Equal(t, wantBaseValue, unwrap(jobBResult))
		}

		// A1 finishes: its step edge leaves the in-memory edge index, so
		// A2 cannot merge onto it via currentIndexKey. If the base
		// is held it survives as a reused-complete edge; otherwise it is deleted
		// and later recreated fresh by A2.
		require.NoError(t, jobA1.Discard())

		// A2: build a step on top of the same shared base. When
		// sameStepVertex it reuses A1's step name/digest (rebuilding
		// the same image); otherwise it uses a distinct name with the same cache
		// seed (modelling a separate build that computes the same cached step).
		jobA2, err := solver.NewJob("a2")
		require.NoError(t, err)

		stepName := "step"
		if !scenario.sameStepVertex {
			stepName = "step-second"
		}
		edgeA2 := stepEdge(stepName, "step-result-uncached", "base-result-uncached", scenario.ignoreBase)
		edgeA2.Vertex.(*vertex).setupCallCounters()

		jobA2Result, err := jobA2.Build(ctx, edgeA2)
		require.NoError(t, err)

		secondExecs = *edgeA2.Vertex.(*vertex).execCallCount
		secondResult = unwrap(jobA2Result)

		require.NoError(t, jobA2.Discard())
		if jobB != nil {
			require.NoError(t, jobB.Discard())
		}
		return secondExecs, secondResult
	}

	// Control: with the base recreated fresh, A2 probes the
	// populated cache and reuses A1's step result. This confirms the
	// harness is capable of producing the hit. (sameStepVertex is irrelevant
	// here: with no B, A1's step edge is gone from the
	// edge index once discarded either way.)
	t.Run("FreshBaseHits", func(t *testing.T) {
		execs, result := run(t, scenario{sameStepVertex: true})
		require.Equal(t, int64(0), execs, "A2 step should reuse cache when base is recreated fresh")
		require.Equal(t, "step-result", result, "A2 should return A1's cached result")
	})

	// Confidence (scenario 2b): a WARM base. A warm-up build populates base's
	// cache record before A1, so A1's base cache-HITS
	// its own query (it does not execute) and leaves its key in edgeState.keys.
	// When B then holds that base `complete`, the reused base
	// STILL delivers its key via edgeState.keys, so A2 probes and
	// hits. This is the healthy warm path, distinct from the cold bug below: it
	// passes REGARDLESS of the fix, and is kept to confirm the fix does not
	// regress the warm case.
	t.Run("WarmHeldBaseHits", func(t *testing.T) {
		execs, result := run(t, scenario{holdBaseAlive: true, sameStepVertex: true, warmBase: true})
		require.Equal(t, int64(0), execs, "A2 step should reuse cache: a warm held base delivers its key via edgeState.keys even when held complete")
		require.Equal(t, "step-result", result, "A2 should return A1's cached result")
	})

	// Bug: with B holding only the base alive, the shared base is reused
	// already `complete`; on unfixed code it delivers edgeState.keys=0 while its
	// result carries the key. A2 must still reuse the cached step result.
	// On unfixed code it re-executes; with the completion-time key append in
	// processExecReq it hits.
	t.Run("HeldCompleteBaseMisses", func(t *testing.T) {
		// SameStepVertex models rebuilding the *same* image: A2's
		// step reuses A1's step name/digest. The base is held
		// `complete` by B, but A1's Discard() has
		// already released its step edge from the edge index, so A2's
		// step edge must resolve through the cache (no live peer to merge
		// onto).
		t.Run("SameStepVertex", func(t *testing.T) {
			execs, result := run(t, scenario{holdBaseAlive: true, sameStepVertex: true})
			require.Equal(t, int64(0), execs, "A2 step re-executed instead of reusing cache (stale complete shared-dep)")
			require.Equal(t, "step-result", result, "A2 should return A1's cached result")
		})

		// DistinctStepVertex models A2 as a *separate* build: to model it, use a
		// different step vertex (different name, same cache seed) that resolves to
		// the same cache id. Like SameStepVertex it relies on A1's
		// Discard() to release its step edge from the edge index -- the index is
		// keyed on the cache key, not the vertex digest, so a distinct digest
		// alone would not close the merge path; the discard does.
		t.Run("DistinctStepVertex", func(t *testing.T) {
			execs, result := run(t, scenario{holdBaseAlive: true})
			require.Equal(t, int64(0), execs, "A2 step re-executed instead of reusing cache (stale complete shared-dep)")
			require.Equal(t, "step-result", result, "A2 should return A1's cached result")
		})
	})

	// Safety (the ignore-cache guard): `--no-cache` on the *dependency* must
	// survive the fix. B holds the shared base `complete`, but the base carries
	// IgnoreCache (as `--no-cache` / `--no-cache-filter=base` would set). An
	// ignore-cache base still executes and Saves, so its result carries cache
	// keys -- but it must NOT advertise them in edgeState.keys, or step would
	// wrongly reuse A1's record. So A2's step must RE-EXECUTE.
	//
	// This holds with or without the broader fix; it exists to lock in the
	// IgnoreCache guard on the completion-time key append in processExecReq.
	// Remove that guard and A2 reuses the step, dropping execs to 0 -- which is
	// how this test discriminates it.
	t.Run("HeldCompleteIgnoreCacheBaseReExecutes", func(t *testing.T) {
		execs, _ := run(t, scenario{holdBaseAlive: true, sameStepVertex: true, ignoreBase: true})
		require.NotEqual(t, int64(0), execs, "A2 step must re-run: base is --no-cache, so its result key must not seed reuse")
	})
}
