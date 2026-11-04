package llbsolver

import (
	"context"
	"runtime"
	"sync/atomic"
	"testing"

	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/frontend"
	"github.com/moby/buildkit/solver"
	"github.com/moby/buildkit/solver/llbsolver/cdidevices"
	llberrdefs "github.com/moby/buildkit/solver/llbsolver/errdefs"
	"github.com/moby/buildkit/worker"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
)

type proxyErrorWorker struct{ worker.Worker }

func (proxyErrorWorker) CDIManager() *cdidevices.Manager { return nil }

type proxyErrorRef struct {
	releases      *atomic.Int64
	cloneReleases *atomic.Int64
	clones        *atomic.Int64
	released      atomic.Bool
	liveRefs      *atomic.Int64
}

func (*proxyErrorRef) ID() string { return "failed-snapshot" }
func (*proxyErrorRef) Sys() any   { return nil }
func (r *proxyErrorRef) Clone() solver.Result {
	if r.liveRefs == nil {
		if r.released.Load() {
			panic("Clone on released error handle")
		}
	} else {
		// Native worker refs can clone a released handle while another ref
		// still pins the underlying snapshot.
		if r.liveRefs.Load() == 0 {
			panic("Clone without a live error snapshot")
		}
		r.liveRefs.Add(1)
	}
	r.clones.Add(1)
	return &proxyErrorRef{releases: r.cloneReleases, cloneReleases: r.cloneReleases, clones: r.clones, liveRefs: r.liveRefs}
}
func (r *proxyErrorRef) Release(context.Context) error {
	r.releases.Add(1)
	if r.released.Swap(true) {
		return errors.New("error handle released twice")
	}
	if r.liveRefs != nil {
		r.liveRefs.Add(-1)
	}
	return nil
}

type proxyErrorOp struct {
	err   error
	where string
	key   digest.Digest
}

func (o *proxyErrorOp) CacheMap(context.Context, solver.JobContext, int) (*solver.CacheMap, bool, error) {
	if o.where == "cachemap" {
		return nil, false, o.err
	}
	key := o.key
	if key == "" {
		key = digest.FromString("proxy error")
	}
	return &solver.CacheMap{Digest: key}, true, nil
}
func (o *proxyErrorOp) Exec(context.Context, solver.JobContext, []solver.Result) ([]solver.Result, error) {
	return nil, o.err
}
func (o *proxyErrorOp) Acquire(context.Context) (solver.ReleaseFunc, error) {
	if o.where == "acquire" {
		return nil, o.err
	}
	return func() {}, nil
}

func TestProxyExecErrorReleased(t *testing.T) {
	for _, where := range []string{"cachemap", "acquire", "exec"} {
		t.Run(where, func(t *testing.T) {
			var releases, cloneReleases, clones atomic.Int64
			ee := llberrdefs.WithExecError(errors.New("failed op"), []solver.Result{&proxyErrorRef{releases: &releases, cloneReleases: &cloneReleases, clones: &clones}}, nil)
			// Keep ee reachable while asserting deterministic cleanup. Its finalizer
			// cannot be the fallback that makes the assertion pass.
			defer runtime.KeepAlive(ee)
			s := solver.NewSolver(solver.SolverOpt{ResolveOpFunc: func(solver.Vertex, solver.Builder) (solver.Op, error) {
				return &proxyErrorOp{err: ee, where: where}, nil
			}})
			defer s.Close()
			j, err := s.NewJob("j")
			require.NoError(t, err)
			defer j.Discard()
			b := &provenanceBridge{llbBridge: &llbBridge{builder: j, resolveWorker: func() (worker.Worker, error) { return proxyErrorWorker{}, nil }}}
			def, err := llb.Scratch().File(llb.Mkdir("/x", 0755)).Marshal(t.Context())
			require.NoError(t, err)
			proxies := make([]*resultProxy, 8)
			for i := range proxies {
				proxies[i] = newResultProxy(b, frontend.SolveRequest{Definition: def.ToPB()})
				_, err = proxies[i].Result(t.Context())
				require.ErrorIs(t, err, ee)
				_, err = proxies[i].loadResult(t.Context())
				require.ErrorIs(t, err, ee)
				require.Len(t, proxies[i].execErrors, 1)
			}
			for _, rp := range proxies {
				require.NoError(t, rp.Release(t.Context()))
			}
			require.NoError(t, j.Discard())
			require.Equal(t, int64(1), releases.Load(), "all proxies together must release the original handle exactly once")
			require.Equal(t, clones.Load(), cloneReleases.Load())
		})
	}
}

func TestSharedExecErrorHandlesSurviveFirstJob(t *testing.T) {
	var releases, cloneReleases, clones atomic.Int64
	var liveRefs atomic.Int64
	liveRefs.Store(1)
	ref := &proxyErrorRef{releases: &releases, cloneReleases: &cloneReleases, clones: &clones, liveRefs: &liveRefs}
	buildErr := llberrdefs.WithExecError(errors.New("shared failure"), []solver.Result{ref}, nil)
	var ee *llberrdefs.ExecError
	require.ErrorAs(t, buildErr, &ee)
	defer ee.Release()
	s := solver.NewSolver(solver.SolverOpt{ResolveOpFunc: func(v solver.Vertex, _ solver.Builder) (solver.Op, error) {
		return &proxyErrorOp{err: buildErr, where: "exec", key: v.Digest()}, nil
	}})
	defer s.Close()
	a, err := s.NewJob("a")
	require.NoError(t, err)
	defer a.Discard()
	b, err := s.NewJob("b")
	require.NoError(t, err)
	defer b.Discard()
	edge := func(path string) solver.Edge {
		def, err := llb.Scratch().File(llb.Mkdir(path, 0o755)).Marshal(t.Context())
		require.NoError(t, err)
		e, err := Load(t.Context(), def.ToPB(), nil)
		require.NoError(t, err)
		return e
	}
	_, err = a.Build(t.Context(), edge("/first"))
	require.ErrorIs(t, err, buildErr)
	second := edge("/second")
	_, err = b.Build(t.Context(), second)
	require.ErrorIs(t, err, buildErr)
	// Release the error's originals before discarding the first state. Only
	// the second state's pin may keep the snapshot available afterward.
	require.NoError(t, ee.Release())
	require.NoError(t, a.Discard())
	require.Equal(t, int64(1), liveRefs.Load())
	_, err = b.Build(t.Context(), second)
	require.ErrorIs(t, err, buildErr)
	require.NotPanics(t, func() {
		require.NoError(t, ee.EachRef(func(r solver.Result) error {
			return r.Clone().Release(t.Context())
		}))
	})
	require.NoError(t, b.Discard())
	require.NoError(t, ee.Release())
	require.Equal(t, int64(1), releases.Load())
	require.Equal(t, clones.Load(), cloneReleases.Load())
	require.Zero(t, liveRefs.Load())
}
