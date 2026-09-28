package solver

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/util/compression"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
)

// countingResults counts the result storage calls that load results.
type countingResults struct {
	CacheResultStorage
	loads       atomic.Int32
	loadRemotes atomic.Int32
}

func (c *countingResults) Load(ctx context.Context, res CacheResult) (Result, error) {
	c.loads.Add(1)
	return c.CacheResultStorage.Load(ctx, res)
}

func (c *countingResults) LoadRemotes(ctx context.Context, res CacheResult, comp *compression.Config, g session.Group) ([]*Remote, error) {
	c.loadRemotes.Add(1)
	return c.CacheResultStorage.LoadRemotes(ctx, res, comp, g)
}

func TestPrepareCacheExport(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	results := &countingResults{CacheResultStorage: NewInMemoryResultStorage()}
	l := NewSolver(SolverOpt{
		ResolveOpFunc: testOpResolver,
		DefaultCache:  NewCacheManager(ctx, identity.NewID(), NewInMemoryCacheStorage(), results),
	})
	defer l.Close()

	j, err := l.NewJob("j0")
	require.NoError(t, err)
	defer func() {
		if j != nil {
			j.Discard()
		}
	}()

	branch := func(v int) Edge {
		return Edge{Vertex: vtxSum(v, vtxOpt{inputs: []Edge{{Vertex: vtxConst(v, vtxOpt{})}}})}
	}
	res, err := j.Build(ctx, Edge{Vertex: vtxSum(1, vtxOpt{
		inputs: []Edge{branch(10), branch(20), branch(30)},
	})})
	require.NoError(t, err)
	require.NoError(t, j.Discard())
	j = nil
	exporter := res.CacheKeys()[0].Exporter

	type calls struct{ loads, loadRemotes, resolves int32 }
	var resolves atomic.Int32
	var orderMu sync.Mutex
	var order []string
	countCalls := func(run func()) calls {
		results.loads.Store(0)
		results.loadRemotes.Store(0)
		resolves.Store(0)
		orderMu.Lock()
		order = nil
		orderMu.Unlock()
		run()
		return calls{results.loads.Load(), results.loadRemotes.Load(), resolves.Load()}
	}
	exportOpt := func(wait func(Result) error) CacheExportOpt {
		opt := testExporterOpts(true)
		resolve := opt.ResolveRemotes
		opt.ResolveRemotes = func(ctx context.Context, r Result) ([]*Remote, error) {
			resolves.Add(1)
			orderMu.Lock()
			order = append(order, r.ID())
			orderMu.Unlock()
			if err := wait(r); err != nil {
				return nil, err
			}
			return resolve(ctx, r)
		}
		return opt
	}
	summary := func(target *testExporterTarget) map[digest.Digest][2]int {
		m := map[digest.Digest][2]int{}
		for _, r := range target.records {
			v := m[r.dgst]
			m[r.dgst] = [2]int{v[0] + r.results, v[1] + r.links}
		}
		return m
	}

	plainTarget := newTestExporterTarget()
	plain := countCalls(func() {
		_, err = exporter.ExportTo(ctx, plainTarget, exportOpt(func(Result) error { return nil }))
		require.NoError(t, err)
	})
	require.Equal(t, int32(4), plain.resolves, "the root and the three branch results")

	// The three branches are independent: each resolution waits for the other
	// two, which only completes if they are prepared concurrently.
	var arrivals sync.WaitGroup
	arrivals.Add(3)
	all := make(chan struct{})
	go func() {
		arrivals.Wait()
		close(all)
	}()
	var arrived sync.Map
	preparedTarget := newTestExporterTarget()
	prepared := countCalls(func() {
		opt := exportOpt(func(r Result) error {
			if r.ID() == res.ID() {
				return nil
			}
			if _, loaded := arrived.LoadOrStore(r.ID(), struct{}{}); !loaded {
				arrivals.Done()
			}
			select {
			case <-all:
				return nil
			case <-time.After(10 * time.Second):
				return errors.New("branch results were not prepared concurrently")
			}
		})
		p, err := PrepareCacheExport(ctx, exporter, opt, 4)
		require.NoError(t, err)
		opt.Prepared = p
		_, err = exporter.ExportTo(ctx, preparedTarget, opt)
		require.NoError(t, err)
	})

	require.Equal(t, plain, prepared, "preparing does not load or resolve any result twice")
	require.Equal(t, res.ID(), order[0], "like ExportTo, a record is prepared before its dependencies")
	require.Equal(t, summary(plainTarget), summary(preparedTarget))
}
