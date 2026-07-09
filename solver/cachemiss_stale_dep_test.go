package solver

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCacheMissStaleCompleteSharedDep reproduces the "stale complete
// shared-dependency" cache miss (moby/buildkit#4674 family) in-process.
//
// Graph (identical for every build): a shared leaf "base" and one op "step" on
// top of it (arrow = "is an input of"):
//
//	"base" --> "step"
//
// A1 builds step -> base against an empty cache. base executes, and its cache
// key ends up living only on base's *result*, not on edgeState.keys (a leaf that
// executed against an empty cache never appended a key, and a completed edge
// never re-queries). Both base and step are saved. A2 then builds a step with
// the SAME cache seed on the SAME shared base and should reuse the cached step.
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
//     already `complete`, and is reused by A2. It delivers edgeState.keys=0 while
//     its result carries the key -- so pre-fix probeCache sees an empty slice,
//     noCacheMatchPossible latches, and A2 re-executes; post-fix probeResultCache
//     reads the result key and A2 hits.
func TestCacheMissStaleCompleteSharedDep(t *testing.T) {
	t.Parallel()

	// baseEdge builds a leaf "base" vertex with a fixed digest and cache seed so
	// it is a single shared active vertex across builds. value differs per build
	// only to prove the returned result came from A1's cache. ignoreCache marks
	// the base as `--no-cache` (used by the ignore-cache safety subtest).
	baseEdge := func(value string, ignoreCache bool) Edge {
		return Edge{Vertex: vtx(vtxOpt{
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
	stepEdge := func(stepName, value, baseValue string, ignoreBase bool) Edge {
		return Edge{Vertex: vtx(vtxOpt{
			name:         stepName,
			cacheKeySeed: "step-seed",
			value:        value,
			inputs:       []Edge{baseEdge(baseValue, ignoreBase)},
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
		ctx := context.TODO()

		cacheManager := newTrackingCacheManager(NewInMemoryCacheManager())
		solver := NewSolver(SolverOpt{
			ResolveOpFunc: testOpResolver,
			DefaultCache:  cacheManager,
		})
		defer solver.Close()

		// warmBase models a WARM builder (scenario 2b): busybox is already
		// pulled, so base's persistent-cache record already exists before
		// A1. A throwaway build of base alone (then discarded) Saves that
		// record. A1's base then cache-HITS its own query -- it does
		// NOT execute and leaves its key in edgeState.keys -- whereas on a cold
		// cache base EXECUTES and its key lives only on the result. The warm-up's
		// distinct value ("base-result-warmup") lets us confirm A1's
		// base came from the cache rather than executing.
		if scenario.warmBase {
			warmJob, err := solver.NewJob("warmup")
			require.NoError(t, err)
			_, err = warmJob.Build(ctx, baseEdge("base-result-warmup", scenario.ignoreBase))
			require.NoError(t, err)
			require.NoError(t, warmJob.Discard())
		}

		// A1: build step -> base. Cold cache: both execute and are saved,
		// base ending `complete` with its key on its result only. Warm cache: base
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
		var jobB *Job
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
		t.Parallel()
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
	// passes REGARDLESS of the completed-dep result-key probe fix, and is kept to
	// confirm the fix does not regress the warm case.
	t.Run("WarmHeldBaseHits", func(t *testing.T) {
		t.Parallel()
		execs, result := run(t, scenario{holdBaseAlive: true, sameStepVertex: true, warmBase: true})
		require.Equal(t, int64(0), execs, "A2 step should reuse cache: a warm held base delivers its key via edgeState.keys even when held complete")
		require.Equal(t, "step-result", result, "A2 should return A1's cached result")
	})

	// Bug: with B holding only the base alive, the shared base
	// is reused already `complete` and delivers edgeState.keys=0 while its result
	// carries the key. A2 must still reuse the cached step result.
	// On current code it re-executes; with the completed-dep result-key probe fix
	// it hits.
	t.Run("HeldCompleteBaseMisses", func(t *testing.T) {
		t.Parallel()

		// SameStepVertex models rebuilding the *same* image: A2's
		// step reuses A1's step name/digest. The base is held
		// `complete` by B, but A1's Discard() has
		// already released its step edge from the edge index, so A2's
		// step edge must resolve through the cache (no live peer to merge
		// onto).
		t.Run("SameStepVertex", func(t *testing.T) {
			t.Parallel()
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
			t.Parallel()
			execs, result := run(t, scenario{holdBaseAlive: true})
			require.Equal(t, int64(0), execs, "A2 step re-executed instead of reusing cache (stale complete shared-dep)")
			require.Equal(t, "step-result", result, "A2 should return A1's cached result")
		})
	})

	// Safety (the ignore-cache guard): `--no-cache` on the *dependency* must
	// survive the fix. B holds the shared base `complete`, but the base carries
	// IgnoreCache (as `--no-cache` / `--no-cache-filter=base` would set). An
	// ignore-cache base still executes and Saves, so its result carries cache
	// keys -- but probeResultCache must NOT probe them, or step would wrongly
	// reuse A1's record. So A2's step must RE-EXECUTE.
	//
	// This holds with or without the broader fix; it exists to lock in
	// probeResultCache's dependency-IgnoreCache guard. Remove that guard and A2
	// reuses the step, dropping execs to 0 -- which is how this test discriminates
	// it.
	t.Run("HeldCompleteIgnoreCacheBaseReExecutes", func(t *testing.T) {
		t.Parallel()
		execs, _ := run(t, scenario{holdBaseAlive: true, sameStepVertex: true, ignoreBase: true})
		require.NotEqual(t, int64(0), execs, "A2 step must re-run: base is --no-cache, so its result key must not seed reuse")
	})
}
