package solver

import (
	"github.com/moby/buildkit/cache/cacheimport"
	"github.com/moby/buildkit/frontend"
	"github.com/moby/buildkit/worker"
)

// DetermineVertexWorker determines worker for a vertex.
// Currently, constraint is just ignored.
// Also we need to track the workers of the inputs.
func determineVertexWorker(wc *worker.Controller, v Vertex) (worker.Worker, error) {
	// TODO: multiworker
	return wc.GetDefault()
}

type LLBOpt struct {
	WorkerController *worker.Controller
	Frontends        map[string]frontend.Frontend // used by nested invocations
	CacheExporter    *cacheimport.CacheExporter
	CacheImporter    *cacheimport.CacheImporter
}

func NewLLBOpSolver(opt LLBOpt) *Solver {
	var s *Solver
	s = New(func(v Vertex) (Op, error) {
		// TODO: in reality, worker should be determined already and passed into this function(or this function would be removed)
		w, err := determineVertexWorker(opt.WorkerController, v)
		if err != nil {
			return nil, err
		}
		return w.Resolve(v, s)
	}, opt.WorkerController, determineVertexWorker, opt.Frontends, opt.CacheExporter, opt.CacheImporter)
	return s
}
