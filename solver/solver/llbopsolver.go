package solver

import (
	"github.com/moby/buildkit/frontend"
	solver "github.com/moby/buildkit/solver"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/solver/solver/llbop"
	"github.com/moby/buildkit/worker"
)

// DetermineVertexWorker determines worker for a vertex.
// Currently, constraint is just ignored.
// Also we need to track the workers of the inputs.
func DetermineVertexWorker(wc *worker.Controller, v solver.Vertex) (*worker.Worker, error) {
	// TODO: multiworker
	return wc.GetDefault()
}

func NewLLBOpSolver(wc *worker.Controller, frontends map[string]frontend.Frontend) solver.Solver {
	var s *Solver
	s = New(func(v solver.Vertex) (solver.Op, error) {
		w, err := DetermineVertexWorker(wc, v)
		if err != nil {
			return nil, err
		}
		switch op := v.Sys().(type) {
		case *pb.Op_Source:
			return llbop.NewSourceOp(v, op, w.SourceManager)
		case *pb.Op_Exec:
			return llbop.NewExecOp(v, op, w.CacheManager, w.Executor)
		case *pb.Op_Build:
			return llbop.NewBuildOp(v, op, s)
		default:
			return nil, nil
		}
	}, wc, DetermineVertexWorker, frontends)
	return s
}
