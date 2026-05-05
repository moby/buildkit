package gateway

import (
	"context"
	"sync"
	"time"

	"github.com/moby/buildkit/client/buildid"
	"github.com/moby/buildkit/frontend/gateway"
	gwapi "github.com/moby/buildkit/frontend/gateway/pb"
	"github.com/moby/buildkit/solver/errdefs"
	"github.com/pkg/errors"
	"google.golang.org/grpc"
)

type llbBridgeForwarderRegistrar struct {
	// notifyCh is the notification channel that gets closed when
	// the bridge is registered.
	notifyCh chan struct{}

	bridge gateway.LLBBridgeForwarder
	err    error

	mu sync.Mutex
}

func newLLBBridgeForwarderRegistrar() *llbBridgeForwarderRegistrar {
	return &llbBridgeForwarderRegistrar{
		notifyCh: make(chan struct{}),
	}
}

func (br *llbBridgeForwarderRegistrar) Register(bridge gateway.LLBBridgeForwarder, err error) {
	br.mu.Lock()
	defer br.mu.Unlock()

	if br.bridge != nil && br.err != nil {
		return
	}

	br.bridge = bridge
	br.err = err
	close(br.notifyCh)
}

type GatewayForwarder struct {
	mu     sync.Mutex
	builds map[string]*llbBridgeForwarderRegistrar
}

func NewGatewayForwarder() *GatewayForwarder {
	gwf := &GatewayForwarder{
		builds: map[string]*llbBridgeForwarderRegistrar{},
	}
	return gwf
}

func (gwf *GatewayForwarder) Register(server *grpc.Server) {
	gwapi.RegisterLLBBridgeServer(server, gwf)
}

func (gwf *GatewayForwarder) RegisterBuild(ctx context.Context, id string, bridge gateway.LLBBridgeForwarder) {
	fwd := gwf.getOrCreateRegistrar(id, nil)
	fwd.Register(bridge, nil)
}

func (gwf *GatewayForwarder) UnregisterBuild(ctx context.Context, id string) {
	gwf.mu.Lock()
	defer gwf.mu.Unlock()

	delete(gwf.builds, id)
}

// getOrCreateRegistrar will create a registrar with the given id to be retrieved at a later time.
// The same id will return the same registrar.
//
// If the registrar is newly created, the onCreate function is invoked in a separate goroutine
// if it is present. If nil, this function is ignored.
func (gwf *GatewayForwarder) getOrCreateRegistrar(id string, onCreate func(*llbBridgeForwarderRegistrar)) *llbBridgeForwarderRegistrar {
	gwf.mu.Lock()
	defer gwf.mu.Unlock()

	fwd, ok := gwf.builds[id]
	if !ok {
		fwd = newLLBBridgeForwarderRegistrar()
		gwf.builds[id] = fwd

		if onCreate != nil {
			go onCreate(fwd)
		}
	}
	return fwd
}

func (gwf *GatewayForwarder) lookupForwarder(ctx context.Context) (gateway.LLBBridgeForwarder, error) {
	bid := buildid.FromIncomingContext(ctx)
	if bid == "" {
		return nil, errors.New("no buildid found in context")
	}

	onCreate := func(fwd *llbBridgeForwarderRegistrar) {
		select {
		case <-fwd.notifyCh:
			return
		case <-time.After(3 * time.Second):
			fwd.Register(nil, errors.WithStack(errdefs.NewUnknownJobError(bid)))
			gwf.UnregisterBuild(context.Background(), bid)
		}
	}

	fwd := gwf.getOrCreateRegistrar(bid, onCreate)

	select {
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	case <-fwd.notifyCh:
		return fwd.bridge, fwd.err
	}
}

func (gwf *GatewayForwarder) ResolveImageConfig(ctx context.Context, req *gwapi.ResolveImageConfigRequest) (*gwapi.ResolveImageConfigResponse, error) {
	fwd, err := gwf.lookupForwarder(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "forwarding ResolveImageConfig")
	}

	return fwd.ResolveImageConfig(ctx, req)
}

func (gwf *GatewayForwarder) ResolveSourceMeta(ctx context.Context, req *gwapi.ResolveSourceMetaRequest) (*gwapi.ResolveSourceMetaResponse, error) {
	fwd, err := gwf.lookupForwarder(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "forwarding ResolveSourceMeta")
	}

	return fwd.ResolveSourceMeta(ctx, req)
}

func (gwf *GatewayForwarder) Solve(ctx context.Context, req *gwapi.SolveRequest) (*gwapi.SolveResponse, error) {
	fwd, err := gwf.lookupForwarder(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "forwarding Solve")
	}

	return fwd.Solve(ctx, req)
}

func (gwf *GatewayForwarder) ReadFile(ctx context.Context, req *gwapi.ReadFileRequest) (*gwapi.ReadFileResponse, error) {
	fwd, err := gwf.lookupForwarder(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "forwarding ReadFile")
	}
	return fwd.ReadFile(ctx, req)
}

func (gwf *GatewayForwarder) Evaluate(ctx context.Context, req *gwapi.EvaluateRequest) (*gwapi.EvaluateResponse, error) {
	fwd, err := gwf.lookupForwarder(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "forwarding Evaluate")
	}
	return fwd.Evaluate(ctx, req)
}

func (gwf *GatewayForwarder) Ping(ctx context.Context, req *gwapi.PingRequest) (*gwapi.PongResponse, error) {
	fwd, err := gwf.lookupForwarder(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "forwarding Ping")
	}
	return fwd.Ping(ctx, req)
}

func (gwf *GatewayForwarder) Return(ctx context.Context, req *gwapi.ReturnRequest) (*gwapi.ReturnResponse, error) {
	fwd, err := gwf.lookupForwarder(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "forwarding Return")
	}
	res, err := fwd.Return(ctx, req)
	return res, err
}

func (gwf *GatewayForwarder) Inputs(ctx context.Context, req *gwapi.InputsRequest) (*gwapi.InputsResponse, error) {
	fwd, err := gwf.lookupForwarder(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "forwarding Inputs")
	}
	res, err := fwd.Inputs(ctx, req)
	return res, err
}

func (gwf *GatewayForwarder) ReadDir(ctx context.Context, req *gwapi.ReadDirRequest) (*gwapi.ReadDirResponse, error) {
	fwd, err := gwf.lookupForwarder(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "forwarding ReadDir")
	}
	return fwd.ReadDir(ctx, req)
}

func (gwf *GatewayForwarder) StatFile(ctx context.Context, req *gwapi.StatFileRequest) (*gwapi.StatFileResponse, error) {
	fwd, err := gwf.lookupForwarder(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "forwarding StatFile")
	}
	return fwd.StatFile(ctx, req)
}

func (gwf *GatewayForwarder) NewContainer(ctx context.Context, req *gwapi.NewContainerRequest) (*gwapi.NewContainerResponse, error) {
	fwd, err := gwf.lookupForwarder(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "forwarding NewContainer")
	}
	return fwd.NewContainer(ctx, req)
}

func (gwf *GatewayForwarder) ReleaseContainer(ctx context.Context, req *gwapi.ReleaseContainerRequest) (*gwapi.ReleaseContainerResponse, error) {
	fwd, err := gwf.lookupForwarder(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "forwarding ReleaseContainer")
	}
	return fwd.ReleaseContainer(ctx, req)
}

func (gwf *GatewayForwarder) ReadFileContainer(ctx context.Context, req *gwapi.ReadFileRequest) (*gwapi.ReadFileResponse, error) {
	fwd, err := gwf.lookupForwarder(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "forwarding ReadFileContainer")
	}
	return fwd.ReadFileContainer(ctx, req)
}

func (gwf *GatewayForwarder) ReadDirContainer(ctx context.Context, req *gwapi.ReadDirRequest) (*gwapi.ReadDirResponse, error) {
	fwd, err := gwf.lookupForwarder(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "forwarding ReadDirContainer")
	}
	return fwd.ReadDirContainer(ctx, req)
}

func (gwf *GatewayForwarder) StatFileContainer(ctx context.Context, req *gwapi.StatFileRequest) (*gwapi.StatFileResponse, error) {
	fwd, err := gwf.lookupForwarder(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "forwarding StatFileContainer")
	}
	return fwd.StatFileContainer(ctx, req)
}

func (gwf *GatewayForwarder) ExecProcess(srv gwapi.LLBBridge_ExecProcessServer) error {
	fwd, err := gwf.lookupForwarder(srv.Context())
	if err != nil {
		return errors.Wrap(err, "forwarding ExecProcess")
	}
	return fwd.ExecProcess(srv)
}

func (gwf *GatewayForwarder) Warn(ctx context.Context, req *gwapi.WarnRequest) (*gwapi.WarnResponse, error) {
	fwd, err := gwf.lookupForwarder(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "forwarding Warn")
	}
	return fwd.Warn(ctx, req)
}
