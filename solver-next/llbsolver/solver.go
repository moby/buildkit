package llbsolver

import (
	"context"
	"time"

	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/cache/cacheimport"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/exporter"
	"github.com/moby/buildkit/frontend"
	"github.com/moby/buildkit/session"
	solver "github.com/moby/buildkit/solver-next"
	"github.com/moby/buildkit/util/progress"
	"github.com/moby/buildkit/worker"
	"github.com/pkg/errors"
)

type ExporterRequest struct {
	Exporter      exporter.ExporterInstance
	CacheExporter *cacheimport.RegistryCacheExporter
}

// ResolveWorkerFunc returns default worker for the temporary default non-distributed use cases
type ResolveWorkerFunc func() (worker.Worker, error)

type Solver struct {
	solver        *solver.JobList // TODO: solver.Solver
	resolveWorker ResolveWorkerFunc
	frontends     map[string]frontend.Frontend
	ci            *cacheimport.CacheImporter
}

func New(wc *worker.Controller, f map[string]frontend.Frontend, cacheStore solver.CacheKeyStorage, ci *cacheimport.CacheImporter) *Solver {
	s := &Solver{
		resolveWorker: defaultResolver(wc),
		frontends:     f,
		ci:            ci,
	}

	results := newCacheResultStorage(wc)

	cache := solver.NewCacheManager("local", cacheStore, results)

	s.solver = solver.NewJobList(solver.SolverOpt{
		ResolveOpFunc: s.resolver(),
		DefaultCache:  cache,
	})
	return s
}

func (s *Solver) resolver() solver.ResolveOpFunc {
	return func(v solver.Vertex, b solver.Builder) (solver.Op, error) {
		w, err := s.resolveWorker()
		if err != nil {
			return nil, err
		}
		return w.ResolveOp(v, s.Bridge(b))
	}
}

func (s *Solver) Bridge(b solver.Builder) frontend.FrontendLLBBridge {
	return &llbBridge{
		builder:       b,
		frontends:     s.frontends,
		resolveWorker: s.resolveWorker,
		ci:            s.ci,
		cms:           map[string]solver.CacheManager{},
	}
}

func (s *Solver) Solve(ctx context.Context, id string, req frontend.SolveRequest, exp ExporterRequest) error {
	j, err := s.solver.NewJob(id)
	if err != nil {
		return err
	}

	defer j.Discard()

	j.SessionID = session.FromContext(ctx)

	res, exporterOpt, err := s.Bridge(j).Solve(ctx, req)
	if err != nil {
		return err
	}

	defer func() {
		if res != nil {
			go res.Release(context.TODO())
		}
	}()

	if exp := exp.Exporter; exp != nil {
		var immutable cache.ImmutableRef
		if res != nil {
			workerRef, ok := res.Sys().(*worker.WorkerRef)
			if !ok {
				return errors.Errorf("invalid reference: %T", res.Sys())
			}
			immutable = workerRef.ImmutableRef
		}

		if err := j.Call(ctx, exp.Name(), func(ctx context.Context) error {
			return exp.Export(ctx, immutable, exporterOpt)
		}); err != nil {
			return err
		}
	}

	if exp := exp.CacheExporter; exp != nil {
		if err := j.Call(ctx, "exporting cache", func(ctx context.Context) error {
			prepareDone := oneOffProgress(ctx, "preparing build cache for export")
			if _, err := res.CacheKey().ExportTo(ctx, exp, workerRefConverter); err != nil {
				return prepareDone(err)
			}
			prepareDone(nil)

			return exp.Finalize(ctx)
		}); err != nil {
			return err
		}
	}

	return err
}

func (s *Solver) Status(ctx context.Context, id string, statusChan chan *client.SolveStatus) error {
	j, err := s.solver.Get(id)
	if err != nil {
		return err
	}
	return j.Status(ctx, statusChan)
}

func defaultResolver(wc *worker.Controller) ResolveWorkerFunc {
	return func() (worker.Worker, error) {
		return wc.GetDefault()
	}
}

func oneOffProgress(ctx context.Context, id string) func(err error) error {
	pw, _, _ := progress.FromContext(ctx)
	now := time.Now()
	st := progress.Status{
		Started: &now,
	}
	pw.Write(id, st)
	return func(err error) error {
		// TODO: set error on status
		now := time.Now()
		st.Completed = &now
		pw.Write(id, st)
		pw.Close()
		return err
	}
}
