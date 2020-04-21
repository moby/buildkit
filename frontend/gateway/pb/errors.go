package moby_buildkit_v1_frontend

import (
	context "context"

	"github.com/moby/buildkit/solver/errdefs"
	grpc "google.golang.org/grpc"
)

func WrapServerErrors(srv LLBBridgeServer) LLBBridgeServer {
	return &wrappedServer{srv}
}

type wrappedServer struct {
	srv LLBBridgeServer
}

func (w *wrappedServer) ResolveImageConfig(ctx context.Context, req *ResolveImageConfigRequest) (*ResolveImageConfigResponse, error) {
	r, err := w.srv.ResolveImageConfig(ctx, req)
	return r, errdefs.ToGRPC(err)
}
func (w *wrappedServer) Solve(ctx context.Context, req *SolveRequest) (*SolveResponse, error) {
	r, err := w.srv.Solve(ctx, req)
	return r, errdefs.ToGRPC(err)
}
func (w *wrappedServer) ReadFile(ctx context.Context, req *ReadFileRequest) (*ReadFileResponse, error) {
	r, err := w.srv.ReadFile(ctx, req)
	return r, errdefs.ToGRPC(err)
}
func (w *wrappedServer) ReadDir(ctx context.Context, req *ReadDirRequest) (*ReadDirResponse, error) {
	r, err := w.srv.ReadDir(ctx, req)
	return r, errdefs.ToGRPC(err)
}
func (w *wrappedServer) StatFile(ctx context.Context, req *StatFileRequest) (*StatFileResponse, error) {
	r, err := w.srv.StatFile(ctx, req)
	return r, errdefs.ToGRPC(err)
}
func (w *wrappedServer) Ping(ctx context.Context, req *PingRequest) (*PongResponse, error) {
	r, err := w.srv.Ping(ctx, req)
	return r, errdefs.ToGRPC(err)
}
func (w *wrappedServer) Return(ctx context.Context, req *ReturnRequest) (*ReturnResponse, error) {
	r, err := w.srv.Return(ctx, req)
	return r, errdefs.ToGRPC(err)
}
func (w *wrappedServer) Inputs(ctx context.Context, req *InputsRequest) (*InputsResponse, error) {
	r, err := w.srv.Inputs(ctx, req)
	return r, errdefs.ToGRPC(err)
}

func WrapClientErrors(c LLBBridgeClient) LLBBridgeClient {
	return &wrappedClient{c}
}

type wrappedClient struct {
	c LLBBridgeClient
}

func (c *wrappedClient) ResolveImageConfig(ctx context.Context, in *ResolveImageConfigRequest, opts ...grpc.CallOption) (*ResolveImageConfigResponse, error) {
	r, err := c.c.ResolveImageConfig(ctx, in, opts...)
	return r, errdefs.FromGRPC(err)
}
func (c *wrappedClient) Solve(ctx context.Context, in *SolveRequest, opts ...grpc.CallOption) (*SolveResponse, error) {
	r, err := c.c.Solve(ctx, in, opts...)
	return r, errdefs.FromGRPC(err)
}
func (c *wrappedClient) ReadFile(ctx context.Context, in *ReadFileRequest, opts ...grpc.CallOption) (*ReadFileResponse, error) {
	r, err := c.c.ReadFile(ctx, in, opts...)
	return r, errdefs.FromGRPC(err)
}
func (c *wrappedClient) ReadDir(ctx context.Context, in *ReadDirRequest, opts ...grpc.CallOption) (*ReadDirResponse, error) {
	r, err := c.c.ReadDir(ctx, in, opts...)
	return r, errdefs.FromGRPC(err)
}
func (c *wrappedClient) StatFile(ctx context.Context, in *StatFileRequest, opts ...grpc.CallOption) (*StatFileResponse, error) {
	r, err := c.c.StatFile(ctx, in, opts...)
	return r, errdefs.FromGRPC(err)
}
func (c *wrappedClient) Ping(ctx context.Context, in *PingRequest, opts ...grpc.CallOption) (*PongResponse, error) {
	r, err := c.c.Ping(ctx, in, opts...)
	return r, errdefs.FromGRPC(err)
}
func (c *wrappedClient) Return(ctx context.Context, in *ReturnRequest, opts ...grpc.CallOption) (*ReturnResponse, error) {
	r, err := c.c.Return(ctx, in, opts...)
	return r, errdefs.FromGRPC(err)
}
func (c *wrappedClient) Inputs(ctx context.Context, in *InputsRequest, opts ...grpc.CallOption) (*InputsResponse, error) {
	r, err := c.c.Inputs(ctx, in, opts...)
	return r, errdefs.FromGRPC(err)
}
