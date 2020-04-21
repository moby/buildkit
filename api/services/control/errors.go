package moby_buildkit_v1

import (
	context "context"

	"github.com/moby/buildkit/solver/errdefs"
	grpc "google.golang.org/grpc"
)

func WrapServerErrors(srv ControlServer) ControlServer {
	return &wrappedServer{srv}
}

type wrappedServer struct {
	srv ControlServer
}

func (s *wrappedServer) DiskUsage(ctx context.Context, req *DiskUsageRequest) (*DiskUsageResponse, error) {
	r, err := s.srv.DiskUsage(ctx, req)
	return r, errdefs.ToGRPC(err)
}
func (s *wrappedServer) Prune(req *PruneRequest, ps Control_PruneServer) error {
	return errdefs.ToGRPC(s.srv.Prune(req, ps))
}
func (s *wrappedServer) Solve(ctx context.Context, req *SolveRequest) (*SolveResponse, error) {
	r, err := s.srv.Solve(ctx, req)
	return r, errdefs.ToGRPC(err)
}
func (s *wrappedServer) Status(req *StatusRequest, ss Control_StatusServer) error {
	return errdefs.ToGRPC(s.srv.Status(req, ss))
}
func (s *wrappedServer) Session(req Control_SessionServer) error {
	return errdefs.ToGRPC(s.srv.Session(req))
}
func (s *wrappedServer) ListWorkers(ctx context.Context, req *ListWorkersRequest) (*ListWorkersResponse, error) {
	r, err := s.srv.ListWorkers(ctx, req)
	return r, errdefs.ToGRPC(err)
}

func WrapClientErrors(c ControlClient) ControlClient {
	return &wrappedClient{c}
}

type wrappedClient struct {
	c ControlClient
}

func (c *wrappedClient) DiskUsage(ctx context.Context, in *DiskUsageRequest, opts ...grpc.CallOption) (*DiskUsageResponse, error) {
	r, err := c.c.DiskUsage(ctx, in, opts...)
	return r, errdefs.FromGRPC(err)
}
func (c *wrappedClient) Prune(ctx context.Context, in *PruneRequest, opts ...grpc.CallOption) (Control_PruneClient, error) {
	r, err := c.c.Prune(ctx, in, opts...)
	return r, errdefs.FromGRPC(err)
}
func (c *wrappedClient) Solve(ctx context.Context, in *SolveRequest, opts ...grpc.CallOption) (*SolveResponse, error) {
	r, err := c.c.Solve(ctx, in, opts...)
	return r, errdefs.FromGRPC(err)
}
func (c *wrappedClient) Status(ctx context.Context, in *StatusRequest, opts ...grpc.CallOption) (Control_StatusClient, error) {
	r, err := c.c.Status(ctx, in, opts...)
	return r, errdefs.FromGRPC(err)
}
func (c *wrappedClient) Session(ctx context.Context, opts ...grpc.CallOption) (Control_SessionClient, error) {
	r, err := c.c.Session(ctx, opts...)
	return r, errdefs.FromGRPC(err)
}
func (c *wrappedClient) ListWorkers(ctx context.Context, in *ListWorkersRequest, opts ...grpc.CallOption) (*ListWorkersResponse, error) {
	r, err := c.c.ListWorkers(ctx, in, opts...)
	return r, errdefs.FromGRPC(err)
}
