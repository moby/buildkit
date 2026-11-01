package gateway

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/moby/buildkit/solver"
	"github.com/moby/buildkit/solver/errdefs"
	llberrdefs "github.com/moby/buildkit/solver/llbsolver/errdefs"
	"github.com/moby/buildkit/worker"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestCachedErrorRefsAreCloned(t *testing.T) {
	for _, kind := range []string{"exec", "slow cache"} {
		t.Run(kind, func(t *testing.T) {
			var clones, cloneReleases, originalReleases atomic.Int32
			newRef := func() *borrowedErrorResult {
				return &borrowedErrorResult{clones: &clones, releases: &originalReleases, cloneReleases: &cloneReleases}
			}
			a, b := newRef(), newRef()
			buildErr := errors.New("build failed")
			var cachedErr error
			if kind == "exec" {
				cachedErr = llberrdefs.WithExecError(buildErr, []solver.Result{a, nil, b}, []solver.Result{a, b})
				var ee *llberrdefs.ExecError
				require.ErrorAs(t, cachedErr, &ee)
				ee.Borrow() // This test owns the original refs.
			} else {
				cachedErr = solver.WrapSlowCache(buildErr, 0, a)
			}
			first := &llbBridgeForwarder{resultByID: map[string]solver.Result{}}
			second := &llbBridgeForwarder{resultByID: map[string]solver.Result{}}
			for _, f := range []*llbBridgeForwarder{first, second} {
				t.Cleanup(func() { f.Discard() })
				var wg sync.WaitGroup
				wrapped := make([]error, 8)
				for i := range wrapped {
					wg.Go(func() { wrapped[i] = f.wrapSolveError(cachedErr) })
				}
				wg.Wait()
				for _, err := range wrapped {
					require.ErrorIs(t, err, buildErr)
					var se *errdefs.SolveError
					require.ErrorAs(t, err, &se)
					if kind == "exec" {
						require.Equal(t, []string{"worker::", "", "worker::"}, se.InputIDs)
						require.Equal(t, []string{"worker::", "worker::"}, se.MountIDs)
					} else {
						require.Equal(t, []string{"worker::"}, se.InputIDs)
					}
				}
			}
			require.Equal(t, int32(2), clones.Load()-cloneReleases.Load(), "each gateway retains one clone per worker ref ID")
			require.Zero(t, originalReleases.Load(), "registration must not consume cached error refs")
			f := first
			f.Discard()
			require.Equal(t, int32(1), clones.Load()-cloneReleases.Load())
			require.ErrorIs(t, second.wrapSolveError(cachedErr), buildErr)
			f = second
			f.Discard()
			require.Equal(t, clones.Load(), cloneReleases.Load())
			require.Zero(t, originalReleases.Load(), "discard must release only gateway-owned clones")
			clonesAfterDiscard := clones.Load()
			require.ErrorIs(t, second.wrapSolveError(cachedErr), buildErr)
			require.Equal(t, clonesAfterDiscard, clones.Load(), "discarded gateways must not acquire new refs")
			require.NoError(t, a.Release(t.Context()))
			require.NoError(t, b.Release(t.Context()))
		})
	}
}

type borrowedErrorWorker struct{ worker.Worker }

func (borrowedErrorWorker) ID() string { return "worker" }

type borrowedErrorResult struct {
	clones        *atomic.Int32
	releases      *atomic.Int32
	cloneReleases *atomic.Int32
	released      atomic.Bool
}

func (*borrowedErrorResult) ID() string { return "worker::" }
func (*borrowedErrorResult) Sys() any   { return &worker.WorkerRef{Worker: borrowedErrorWorker{}} }
func (r *borrowedErrorResult) Clone() solver.Result {
	r.clones.Add(1)
	return &borrowedErrorResult{clones: r.clones, releases: r.cloneReleases, cloneReleases: r.cloneReleases}
}
func (r *borrowedErrorResult) Release(context.Context) error {
	if r.released.Swap(true) {
		return errors.New("error ref released twice")
	}
	r.releases.Add(1)
	return nil
}
