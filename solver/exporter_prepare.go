package solver

import (
	"context"
	"slices"
	"sync"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"
)

// PreparedCacheExport holds the remotes that PrepareCacheExport resolved, so
// ExportTo does not load the same results again.
type PreparedCacheExport struct {
	mu      sync.Mutex
	results map[preparedKey]*preparedResult
}

type preparedKey struct {
	cm *cacheManager
	id string
}

// preparedResult records what ExportTo would get from LoadRemotes and, when
// the record needs its local result, from ResolveRemotes.
type preparedResult struct {
	loaded   []*Remote
	remotes  []*Remote
	resolved bool
}

func (p *PreparedCacheExport) lookup(cm *cacheManager, id string) *preparedResult {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.results[preparedKey{cm: cm, id: id}]
}

func (p *PreparedCacheExport) store(cm *cacheManager, id string, r *preparedResult) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.results[preparedKey{cm: cm, id: id}] = r
}

// loadRemotes returns the remotes already available for res.
func (r *preparedResult) loadRemotes(ctx context.Context, cm *cacheManager, res CacheResult, opt CacheExportOpt) ([]*Remote, error) {
	if r != nil {
		return r.loaded, nil
	}
	return cm.results.LoadRemotes(ctx, res, opt.CompressionOpt, opt.Session)
}

// resolveRemotes resolves the remotes of the local result of res. It reports
// false when the result no longer exists.
func (r *preparedResult) resolveRemotes(ctx context.Context, cm *cacheManager, res CacheResult, opt CacheExportOpt) ([]*Remote, bool, error) {
	if r != nil && r.resolved {
		return r.remotes, true, nil
	}
	lr, err := cm.results.Load(ctx, res)
	if err != nil {
		if errors.Is(err, cerrdefs.ErrNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	remotes, err := opt.ResolveRemotes(ctx, lr)
	if err != nil {
		return nil, false, err
	}
	lr.Release(context.TODO())
	return remotes, true, nil
}

// PrepareCacheExport resolves the remotes that ExportTo will need, in parallel
// across independent records, and returns them for ExportTo to reuse through
// CacheExportOpt.Prepared. ExportTo walks records one at a time, so on a
// mode=max export it creates the blobs of independent stages sequentially.
// Like ExportTo, a record is prepared before its dependencies, which then
// usually find their blobs among the ones created for its chain. Failures are
// not reported: ExportTo resolves those records itself and reports the error,
// so the exported cache is the same.
func PrepareCacheExport(ctx context.Context, e CacheExporter, opt CacheExportOpt, parallelism int) (*PreparedCacheExport, error) {
	if parallelism < 1 {
		parallelism = 1
	}
	eg, egCtx := errgroup.WithContext(ctx)
	p := &exportPreparer{
		eg:       eg,
		ctx:      egCtx,
		opt:      opt,
		sem:      semaphore.NewWeighted(int64(parallelism)),
		visited:  map[CacheExporter]struct{}{},
		prepared: &PreparedCacheExport{results: map[preparedKey]*preparedResult{}},
	}
	p.visit(ctx, e)
	if err := eg.Wait(); err != nil {
		return nil, err
	}
	return p.prepared, nil
}

type exportPreparer struct {
	eg       *errgroup.Group
	ctx      context.Context
	opt      CacheExportOpt
	sem      *semaphore.Weighted
	mu       sync.Mutex
	visited  map[CacheExporter]struct{}
	prepared *PreparedCacheExport
}

// visit prepares ce once, then its dependencies. It mirrors the traversal of
// ExportTo, including how record context options are inherited by
// dependencies but not by secondary exporters.
func (p *exportPreparer) visit(ctx context.Context, ce CacheExporter) {
	p.mu.Lock()
	_, seen := p.visited[ce]
	p.visited[ce] = struct{}{}
	p.mu.Unlock()
	if seen {
		return
	}

	p.eg.Go(func() error {
		switch e := ce.(type) {
		case *mergedExporter:
			for _, e := range e.exporters {
				p.visit(ctx, e)
			}
		case *exporter:
			mainCtx := ctx
			if CacheOptGetterOf(ctx) == nil && e.recordCtxOpts != nil {
				ctx = e.recordCtxOpts(ctx)
			}
			if err := p.prepareRecord(ctx, e); err != nil {
				return err
			}
			for _, deps := range e.k.Deps() {
				for _, dep := range deps {
					p.visit(ctx, dep.CacheKey.Exporter)
				}
			}
			if e.edge != nil {
				for _, de := range e.edge.secondaryExporters {
					p.visit(mainCtx, de.cacheKey.CacheKey.Exporter)
				}
			}
		}
		return nil
	})
}

// prepareRecord loads the remotes of the record ExportTo would export for e
// and resolves its local result when needed.
func (p *exportPreparer) prepareRecord(ctx context.Context, e *exporter) error {
	if e.override != nil && !*e.override {
		return nil
	}
	if len(e.k.Deps()) == 0 && !p.opt.ExportRoots {
		return nil
	}
	if p.opt.Mode == CacheExportModeRemoteOnly {
		return nil
	}

	records := slices.Clone(e.records)
	slices.SortStableFunc(records, compareCacheRecord)
	if e.record != nil {
		records = append([]*CacheRecord{e.record}, records...)
	}
	for _, v := range records {
		cm := v.cacheManager
		res, err := cm.backend.Load(cm.getID(v.key), v.ID)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil
		}
		if err := p.sem.Acquire(p.ctx, 1); err != nil {
			return err
		}
		defer p.sem.Release(1)
		loaded, err := cm.results.LoadRemotes(ctx, res, p.opt.CompressionOpt, p.opt.Session)
		if err != nil {
			return nil
		}
		r := &preparedResult{loaded: loaded}
		var remote *Remote
		if len(loaded) > 0 {
			remote = loaded[0]
		}
		if needsLocalResult(remote, p.opt) {
			remotes, found, err := (*preparedResult)(nil).resolveRemotes(ctx, cm, res, p.opt)
			if err == nil && found {
				r.remotes, r.resolved = remotes, true
			}
		}
		p.prepared.store(cm, res.ID, r)
		return nil
	}
	return nil
}
