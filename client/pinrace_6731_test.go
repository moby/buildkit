package client

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/moby/buildkit/client/llb"
	gateway "github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/util/testutil/integration"
	"github.com/moby/buildkit/util/testutil/workers"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

func init() {
	allTests = append(allTests, testPinRaceIgnoreCacheShift)
}

// testPinRaceIgnoreCacheShift exercises issue #6731 ("provenance: image
// source pin can be read empty during capture").
//
// The race is between captureProvenance's walkProvenance walking through a
// shifted source state and another build that is still inside that state's
// SourceOp.CacheMap (resolver returned, CacheKey fetch in flight, pin not
// yet written). The walker sees a *SourceOp with empty pin, calls
// HTTPIdentifier.Capture, and digest.Parse("") returns "invalid checksum
// digest format".
//
// Three logical builds, run as two physical c.Build calls so the warmup
// and the racer end up in different Jobs with separate ResolverCaches.
// Within a single Job, BuildKit's HTTP source short-circuits the racer's
// CacheKey via the Job's ResolverCache, closing the race window before
// the walker can fire. state[base_D] is kept alive across the two
// c.Builds by holding the warmup frontend open via a channel until the
// race c.Build is done.
//
//  1. warmup (Job 1, c.Build #1) — no IgnoreCache. Evaluates fully so
//     state[base_D], state[mid_M], state[root_R] are populated and
//     state[root_R].edges[0] is Complete. The frontend then blocks on
//     warmupHold so Job 1 keeps state[base_D] alive in actives.
//
//  2. race-creator (Job 2, c.Build #2) — IgnoreCache on the http source
//     + a fresh mid copy-dest so the mid digest differs from warmup's.
//     Load triggers the dgstWithoutCache shift in Solver.loadUnlocked,
//     creating state[base_D']. The fresh mid forces a fresh
//     state[mid_M2] whose vtx.Inputs() points at D', so the scheduler
//     calls state[base_D'].getEdge and starts CacheMap. resolveMetadataRef
//     consults state[base_D'].Lock which only combines Job 2's
//     ResolverCache (empty), so it falls through to the slow HTTP fetch.
//
//  3. walker (same c.Build as race-creator, same Job 2) — IgnoreCache on
//     http + the ORIGINAL mid digest. Load takes the actives[D'] reuse
//     path in loadUnlocked, then scheduler.build finds
//     state[root_R].edges[0] complete from Job 1 and short-circuits.
//     captureProvenance walks via the walker's wrapper graph, reaches
//     state[base_D'] mid-CacheKey, gets past the existing nil-op guard
//     (op and op.op are both non-nil at this point), reads the
//     half-initialised SourceOp, and triggers the digest.Parse error.
//
// The slow HTTP source widens the race window deterministically so no
// custom buildkitd build or env-var instrumentation is needed.
func testPinRaceIgnoreCacheShift(t *testing.T, sb integration.Sandbox) {
	integration.SkipOnPlatform(t, "windows")
	workers.CheckFeatureCompat(t, sb, workers.FeatureMergeDiff)

	const fetchDelay = 1500 * time.Millisecond
	const headStart = 400 * time.Millisecond
	const iterations = 4

	// HTTP server that delays every response and disables caching so the
	// daemon's HTTP source has to do a fresh fetch each time.
	startedAt := time.Now()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Logf("http hit:  method=%s url=%s t+%s", r.Method, r.URL.Path, time.Since(startedAt).Round(time.Millisecond))
		time.Sleep(fetchDelay)
		t.Logf("http resp: method=%s url=%s t+%s", r.Method, r.URL.Path, time.Since(startedAt).Round(time.Millisecond))
		w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Expires", "0")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hello"))
	}))
	defer server.Close()

	c, err := New(sb.Context(), sb.Address())
	require.NoError(t, err)
	defer c.Close()

	for i := range iterations {
		url := fmt.Sprintf("%s/pin-race-%d", server.URL, i)
		err := runPinRaceIteration(sb.Context(), c, url, i, headStart)
		require.NoError(t, err, "iteration %d", i)
	}
}

func runPinRaceIteration(ctx context.Context, c *Client, url string, iter int, headStart time.Duration) error {
	base := llb.HTTP(url)
	baseIC := llb.HTTP(url, llb.IgnoreCache)

	mkChain := func(b llb.State, fresh bool) llb.State {
		dest := "/x"
		if fresh {
			dest = "/x-fresh"
		}
		intermediate := llb.Scratch().File(
			llb.Copy(b, "/pin-race-"+fmt.Sprint(iter), dest),
			llb.WithCustomNamef("pin-race iter=%d mid fresh=%v", iter, fresh),
		)
		return llb.Merge(
			[]llb.State{b, intermediate},
			llb.WithCustomNamef("pin-race iter=%d root fresh=%v", iter, fresh),
		)
	}

	// Step 1: warmup, in its own c.Build (Job 1). The frontend evaluates
	// the warmup chain and then blocks on warmupHold so Job 1 stays alive
	// (keeping state[base_D] in actives) while the race-creator's load
	// runs in a separate c.Build.
	warmupReady := make(chan struct{})
	warmupHold := make(chan struct{})
	warmupFrontend := func(ctx context.Context, gw gateway.Client) (*gateway.Result, error) {
		def, err := mkChain(base, false).Marshal(ctx)
		if err != nil {
			return nil, errors.Wrap(err, "marshal warmup")
		}
		res, err := gw.Solve(ctx, gateway.SolveRequest{Definition: def.ToPB()})
		if err != nil {
			return nil, errors.Wrap(err, "solve warmup")
		}
		ref, err := res.SingleRef()
		if err != nil {
			return nil, err
		}
		if err := ref.Evaluate(ctx); err != nil {
			return nil, errors.Wrap(err, "evaluate warmup")
		}
		close(warmupReady)
		select {
		case <-warmupHold:
		case <-ctx.Done():
			return nil, context.Cause(ctx)
		}
		return res, nil
	}

	var (
		wg        sync.WaitGroup
		warmupErr error
	)
	wg.Go(func() {
		_, warmupErr = c.Build(ctx, SolveOpt{
			FrontendAttrs: map[string]string{
				"attest:provenance": "mode=max",
			},
		}, "pin-race-warmup", warmupFrontend, nil)
	})

	// Wait for warmup to finish its Evaluate. From here on, state[base_D],
	// state[mid_M], state[root_R] are populated and state[root_R].edges[0]
	// is Complete; Job 1 holds them alive.
	select {
	case <-warmupReady:
	case <-ctx.Done():
		close(warmupHold)
		wg.Wait()
		return context.Cause(ctx)
	}

	// Step 2 + 3: race-creator + walker, inside their own c.Build (Job 2,
	// separate ResolverCache from Job 1).
	raceFrontend := func(ctx context.Context, gw gateway.Client) (*gateway.Result, error) {
		racerDef, err := mkChain(baseIC, true).Marshal(ctx)
		if err != nil {
			return nil, errors.Wrap(err, "marshal race-creator")
		}
		racerRes, err := gw.Solve(ctx, gateway.SolveRequest{Definition: racerDef.ToPB()})
		if err != nil {
			return nil, errors.Wrap(err, "solve race-creator")
		}
		racerRef, err := racerRes.SingleRef()
		if err != nil {
			return nil, err
		}

		eg, egCtx := errgroup.WithContext(ctx)
		eg.Go(func() error { return racerRef.Evaluate(egCtx) })

		time.Sleep(headStart)

		walkerDef, err := mkChain(baseIC, false).Marshal(ctx)
		if err != nil {
			_ = eg.Wait()
			return nil, errors.Wrap(err, "marshal walker")
		}
		walkerRes, err := gw.Solve(ctx, gateway.SolveRequest{Definition: walkerDef.ToPB()})
		if err != nil {
			_ = eg.Wait()
			return nil, errors.Wrap(err, "solve walker")
		}
		walkerRef, err := walkerRes.SingleRef()
		if err != nil {
			_ = eg.Wait()
			return nil, err
		}

		walkErr := walkerRef.Evaluate(egCtx)
		_ = eg.Wait()
		if walkErr != nil {
			return nil, walkErr
		}
		return walkerRes, nil
	}

	_, raceErr := c.Build(ctx, SolveOpt{
		FrontendAttrs: map[string]string{
			"attest:provenance": "mode=max",
		},
	}, "pin-race-race", raceFrontend, nil)

	// Release the warmup so its c.Build (and Job 1) can return.
	close(warmupHold)
	wg.Wait()

	if warmupErr != nil {
		return errors.Wrap(warmupErr, "warmup")
	}
	return raceErr
}
