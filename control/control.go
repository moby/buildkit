package control

import (
	"github.com/Sirupsen/logrus"
	"github.com/containerd/containerd/snapshot"
	controlapi "github.com/moby/buildkit/api/services/control"
	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/exporter"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/grpchijack"
	"github.com/moby/buildkit/solver"
	"github.com/moby/buildkit/source"
	"github.com/moby/buildkit/worker"
	"github.com/pkg/errors"
	"golang.org/x/net/context"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
)

type Opt struct {
	Snapshotter      snapshot.Snapshotter
	CacheManager     cache.Manager
	Worker           worker.Worker
	SourceManager    *source.Manager
	InstructionCache solver.InstructionCache
	Exporters        map[string]exporter.Exporter
	SessionManager   *session.Manager
}

type Controller struct { // TODO: ControlService
	opt    Opt
	solver *solver.Solver
}

func NewController(opt Opt) (*Controller, error) {
	c := &Controller{
		opt: opt,
		solver: solver.NewLLBSolver(solver.LLBOpt{
			SourceManager:    opt.SourceManager,
			CacheManager:     opt.CacheManager,
			Worker:           opt.Worker,
			InstructionCache: opt.InstructionCache,
		}),
	}
	return c, nil
}

func (c *Controller) Register(server *grpc.Server) error {
	controlapi.RegisterControlServer(server, c)
	return nil
}

func (c *Controller) DiskUsage(ctx context.Context, _ *controlapi.DiskUsageRequest) (*controlapi.DiskUsageResponse, error) {
	du, err := c.opt.CacheManager.DiskUsage(ctx)
	if err != nil {
		return nil, err
	}

	resp := &controlapi.DiskUsageResponse{}
	for _, r := range du {
		resp.Record = append(resp.Record, &controlapi.UsageRecord{
			ID:      r.ID,
			Mutable: r.Mutable,
			InUse:   r.InUse,
			Size_:   r.Size,
		})
	}
	return resp, nil
}

func (c *Controller) Solve(ctx context.Context, req *controlapi.SolveRequest) (*controlapi.SolveResponse, error) {
	v, err := solver.LoadLLB(req.Definition)
	if err != nil {
		return nil, errors.Wrap(err, "failed to load")
	}

	var expi exporter.ExporterInstance
	if req.Exporter != "" {
		exp, ok := c.opt.Exporters[req.Exporter]
		if !ok {
			return nil, errors.Errorf("exporter %q could not be found", req.Exporter)
		}
		expi, err = exp.Resolve(ctx, req.ExporterAttrs)
		if err != nil {
			return nil, err
		}
	}

	ctx = session.NewContext(ctx, req.Session)

	if err := c.solver.Solve(ctx, req.Ref, v, expi); err != nil {
		return nil, err
	}
	return &controlapi.SolveResponse{}, nil
}

func (c *Controller) Status(req *controlapi.StatusRequest, stream controlapi.Control_StatusServer) error {
	ch := make(chan *client.SolveStatus, 8)

	eg, ctx := errgroup.WithContext(stream.Context())
	eg.Go(func() error {
		return c.solver.Status(ctx, req.Ref, ch)
	})

	eg.Go(func() error {
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case ss, ok := <-ch:
				if !ok {
					return nil
				}
				sr := controlapi.StatusResponse{}
				for _, v := range ss.Vertexes {
					sr.Vertexes = append(sr.Vertexes, &controlapi.Vertex{
						Digest:    v.Digest,
						Inputs:    v.Inputs,
						Name:      v.Name,
						Started:   v.Started,
						Completed: v.Completed,
						Error:     v.Error,
						Cached:    v.Cached,
					})
				}
				for _, v := range ss.Statuses {
					sr.Statuses = append(sr.Statuses, &controlapi.VertexStatus{
						ID:        v.ID,
						Vertex:    v.Vertex,
						Name:      v.Name,
						Current:   v.Current,
						Total:     v.Total,
						Timestamp: v.Timestamp,
						Started:   v.Started,
						Completed: v.Completed,
					})
				}
				for _, v := range ss.Logs {
					sr.Logs = append(sr.Logs, &controlapi.VertexLog{
						Vertex:    v.Vertex,
						Stream:    int64(v.Stream),
						Msg:       v.Data,
						Timestamp: v.Timestamp,
					})
				}
				if err := stream.SendMsg(&sr); err != nil {
					return err
				}
			}
		}
	})

	return eg.Wait()
}

func (c *Controller) Session(stream controlapi.Control_SessionServer) error {
	logrus.Debugf("session started")
	conn, opts := grpchijack.Hijack(stream)
	defer conn.Close()
	err := c.opt.SessionManager.HandleConn(stream.Context(), conn, opts)
	logrus.Debugf("session finished: %v", err)
	return err
}
