package control

import (
	"time"

	"github.com/containerd/containerd/snapshot"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
	controlapi "github.com/tonistiigi/buildkit_poc/api/services/control"
	"github.com/tonistiigi/buildkit_poc/cache"
	"github.com/tonistiigi/buildkit_poc/client"
	"github.com/tonistiigi/buildkit_poc/solver"
	"github.com/tonistiigi/buildkit_poc/source"
	"github.com/tonistiigi/buildkit_poc/worker"
	"golang.org/x/net/context"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
)

type Opt struct {
	Snapshotter   snapshot.Snapshotter
	CacheManager  cache.Manager
	Worker        worker.Worker
	SourceManager *source.Manager
}

type Controller struct { // TODO: ControlService
	opt    Opt
	solver *solver.Solver
}

func NewController(opt Opt) (*Controller, error) {
	c := &Controller{
		opt: opt,
		solver: solver.New(solver.Opt{
			SourceManager: opt.SourceManager,
			CacheManager:  opt.CacheManager,
			Worker:        opt.Worker,
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
	v, err := solver.Load(req.Definition)
	if err != nil {
		return nil, errors.Wrap(err, "failed to load")
	}
	if err := c.solver.Solve(ctx, req.Ref, v); err != nil {
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
						ID:        v.ID.String(),
						Inputs:    digestToString(v.Inputs),
						Name:      v.Name,
						Started:   marshalTimeStamp(v.Started),
						Completed: marshalTimeStamp(v.Completed),
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

func digestToString(dgsts []digest.Digest) (out []string) { // TODO: make proto use digest
	for _, dgst := range dgsts {
		out = append(out, dgst.String())
	}
	return
}

func marshalTimeStamp(tm *time.Time) int64 {
	if tm == nil {
		return 0
	}
	return tm.UnixNano()
}
