package gateway

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/moby/buildkit/client/buildid"
	"github.com/moby/buildkit/frontend/gateway"
	gwapi "github.com/moby/buildkit/frontend/gateway/pb"
	"github.com/moby/buildkit/solver/errdefs"
	pkgerrors "github.com/pkg/errors"
	"google.golang.org/grpc"
)

type GatewayForwarder struct {
	mu         sync.RWMutex
	updateCond *sync.Cond
	builds     map[string]gateway.LLBBridgeForwarder
}

func NewGatewayForwarder() *GatewayForwarder {
	gwf := &GatewayForwarder{
		builds: map[string]gateway.LLBBridgeForwarder{},
	}
	gwf.updateCond = sync.NewCond(gwf.mu.RLocker())
	return gwf
}

func (gwf *GatewayForwarder) Register(server *grpc.Server) {
	gwapi.RegisterLLBBridgeServer(server, gwf)
}

func (gwf *GatewayForwarder) RegisterBuild(ctx context.Context, id string, bridge gateway.LLBBridgeForwarder) error {
	gwf.mu.Lock()
	defer gwf.mu.Unlock()

	if _, ok := gwf.builds[id]; ok {
		return fmt.Errorf("build ID %s exists", id)
	}

	gwf.builds[id] = bridge
	gwf.updateCond.Broadcast()

	return nil
}

func (gwf *GatewayForwarder) UnregisterBuild(ctx context.Context, id string) {
	gwf.mu.Lock()
	defer gwf.mu.Unlock()

	delete(gwf.builds, id)
	gwf.updateCond.Broadcast()
}

func (gwf *GatewayForwarder) lookupForwarder(ctx context.Context) (gateway.LLBBridgeForwarder, error) {
	bid := buildid.FromIncomingContext(ctx)
	if bid == "" {
		return nil, errors.New("no buildid found in context")
	}

	ctx, cancel := context.WithCancelCause(ctx)
	ctx, _ = context.WithTimeoutCause(ctx, 3*time.Second, pkgerrors.WithStack(context.DeadlineExceeded)) //nolint:govet
	defer func() { cancel(pkgerrors.WithStack(context.Canceled)) }()

	go func() {
		<-ctx.Done()
		gwf.mu.Lock()
		gwf.updateCond.Broadcast()
		gwf.mu.Unlock()
	}()

	gwf.mu.RLock()
	defer gwf.mu.RUnlock()
	for {
		select {
		case <-ctx.Done():
			return nil, errdefs.NewUnknownJobError(bid)
		default:
		}
		fwd, ok := gwf.builds[bid]
		if !ok {
			gwf.updateCond.Wait()
			continue
		}
		return fwd, nil
	}
}

func (gwf *GatewayForwarder) ResolveImageConfig(ctx context.Context, req *gwapi.ResolveImageConfigRequest) (*gwapi.ResolveImageConfigResponse, error) {
	fwd, err := gwf.lookupForwarder(ctx)
	if err != nil {
		return nil, fmt.Errorf("forwarding ResolveImageConfig"+": %w", err)
	}

	return fwd.ResolveImageConfig(ctx, req)
}

func (gwf *GatewayForwarder) ResolveSourceMeta(ctx context.Context, req *gwapi.ResolveSourceMetaRequest) (*gwapi.ResolveSourceMetaResponse, error) {
	fwd, err := gwf.lookupForwarder(ctx)
	if err != nil {
		return nil, fmt.Errorf("forwarding ResolveSourceMeta"+": %w", err)
	}

	return fwd.ResolveSourceMeta(ctx, req)
}

func (gwf *GatewayForwarder) Solve(ctx context.Context, req *gwapi.SolveRequest) (*gwapi.SolveResponse, error) {
	fwd, err := gwf.lookupForwarder(ctx)
	if err != nil {
		return nil, fmt.Errorf("forwarding Solve"+": %w", err)
	}

	return fwd.Solve(ctx, req)
}

func (gwf *GatewayForwarder) ReadFile(ctx context.Context, req *gwapi.ReadFileRequest) (*gwapi.ReadFileResponse, error) {
	fwd, err := gwf.lookupForwarder(ctx)
	if err != nil {
		return nil, fmt.Errorf("forwarding ReadFile"+": %w", err)
	}
	return fwd.ReadFile(ctx, req)
}

func (gwf *GatewayForwarder) Evaluate(ctx context.Context, req *gwapi.EvaluateRequest) (*gwapi.EvaluateResponse, error) {
	fwd, err := gwf.lookupForwarder(ctx)
	if err != nil {
		return nil, fmt.Errorf("forwarding Evaluate"+": %w", err)
	}
	return fwd.Evaluate(ctx, req)
}

func (gwf *GatewayForwarder) Ping(ctx context.Context, req *gwapi.PingRequest) (*gwapi.PongResponse, error) {
	fwd, err := gwf.lookupForwarder(ctx)
	if err != nil {
		return nil, fmt.Errorf("forwarding Ping"+": %w", err)
	}
	return fwd.Ping(ctx, req)
}

func (gwf *GatewayForwarder) Return(ctx context.Context, req *gwapi.ReturnRequest) (*gwapi.ReturnResponse, error) {
	fwd, err := gwf.lookupForwarder(ctx)
	if err != nil {
		return nil, fmt.Errorf("forwarding Return"+": %w", err)
	}
	res, err := fwd.Return(ctx, req)
	return res, err
}

func (gwf *GatewayForwarder) Inputs(ctx context.Context, req *gwapi.InputsRequest) (*gwapi.InputsResponse, error) {
	fwd, err := gwf.lookupForwarder(ctx)
	if err != nil {
		return nil, fmt.Errorf("forwarding Inputs"+": %w", err)
	}
	res, err := fwd.Inputs(ctx, req)
	return res, err
}

func (gwf *GatewayForwarder) ReadDir(ctx context.Context, req *gwapi.ReadDirRequest) (*gwapi.ReadDirResponse, error) {
	fwd, err := gwf.lookupForwarder(ctx)
	if err != nil {
		return nil, fmt.Errorf("forwarding ReadDir"+": %w", err)
	}
	return fwd.ReadDir(ctx, req)
}

func (gwf *GatewayForwarder) StatFile(ctx context.Context, req *gwapi.StatFileRequest) (*gwapi.StatFileResponse, error) {
	fwd, err := gwf.lookupForwarder(ctx)
	if err != nil {
		return nil, fmt.Errorf("forwarding StatFile"+": %w", err)
	}
	return fwd.StatFile(ctx, req)
}

func (gwf *GatewayForwarder) NewContainer(ctx context.Context, req *gwapi.NewContainerRequest) (*gwapi.NewContainerResponse, error) {
	fwd, err := gwf.lookupForwarder(ctx)
	if err != nil {
		return nil, fmt.Errorf("forwarding NewContainer"+": %w", err)
	}
	return fwd.NewContainer(ctx, req)
}

func (gwf *GatewayForwarder) ReleaseContainer(ctx context.Context, req *gwapi.ReleaseContainerRequest) (*gwapi.ReleaseContainerResponse, error) {
	fwd, err := gwf.lookupForwarder(ctx)
	if err != nil {
		return nil, fmt.Errorf("forwarding ReleaseContainer"+": %w", err)
	}
	return fwd.ReleaseContainer(ctx, req)
}

func (gwf *GatewayForwarder) ExecProcess(srv gwapi.LLBBridge_ExecProcessServer) error {
	fwd, err := gwf.lookupForwarder(srv.Context())
	if err != nil {
		return fmt.Errorf("forwarding ExecProcess"+": %w", err)
	}
	return fwd.ExecProcess(srv)
}

func (gwf *GatewayForwarder) Warn(ctx context.Context, req *gwapi.WarnRequest) (*gwapi.WarnResponse, error) {
	fwd, err := gwf.lookupForwarder(ctx)
	if err != nil {
		return nil, fmt.Errorf("forwarding Warn"+": %w", err)
	}
	return fwd.Warn(ctx, req)
}
