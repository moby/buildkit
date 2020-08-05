package gateway

import (
	"context"
	"strings"
	"sync"

	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/executor"
	"github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/solver"
	opspb "github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/stack"
	utilsystem "github.com/moby/buildkit/util/system"
	"github.com/moby/buildkit/worker"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"golang.org/x/sync/errgroup"
)

type Container interface {
	client.Container
	// OnRelease allows callbacks to free up resources when the container exits.
	// The functions are called in LIFO order.
	OnRelease(func() error)
}

type NewContainerRequest struct {
	ContainerID  string
	NetMode      opspb.NetMode
	SecurityMode opspb.SecurityMode
	Mounts       []Mount
}

// Mount used for the gateway.Container is nearly identical to the client.Mount
// except is has a RefProxy instead of Ref to allow for a common abstraction
// between gateway clients.
type Mount struct {
	Dest      string
	Selector  string
	Readonly  bool
	MountType opspb.MountType
	RefProxy  solver.ResultProxy
}

func NewContainer(ctx context.Context, e executor.Executor, req NewContainerRequest) (Container, error) {
	ctx, cancel := context.WithCancel(ctx)
	eg, ctx := errgroup.WithContext(ctx)
	ctr := &gatewayContainer{
		id:           req.ContainerID,
		netMode:      req.NetMode,
		securityMode: req.SecurityMode,
		executor:     e,
		errGroup:     eg,
		ctx:          ctx,
		cancel:       cancel,
	}

	for _, m := range req.Mounts {
		res, err := m.RefProxy.Result(ctx)
		if err != nil {
			return nil, stack.Enable(err)
		}
		workerRef, ok := res.Sys().(*worker.WorkerRef)
		if !ok {
			return nil, stack.Enable(errors.Errorf("invalid reference for exec %T", res.Sys()))
		}

		execMount := executor.Mount{
			Src:      workerRef.ImmutableRef,
			Selector: m.Selector,
			Dest:     m.Dest,
			Readonly: m.Readonly,
		}
		if !m.Readonly {
			ref, err := workerRef.Worker.CacheManager().New(ctx, workerRef.ImmutableRef)
			if err != nil {
				return nil, stack.Enable(err)
			}
			ctr.OnRelease(func() error {
				return stack.Enable(ref.Release(context.TODO()))
			})
			execMount.Src = ref
		}

		if m.Dest == "/" {
			ctr.rootFS = execMount.Src
		} else {
			ctr.mounts = append(ctr.mounts, execMount)
		}
	}

	return ctr, nil
}

type gatewayContainer struct {
	id           string
	netMode      opspb.NetMode
	securityMode opspb.SecurityMode
	rootFS       cache.Mountable
	mounts       []executor.Mount
	executor     executor.Executor
	started      bool
	errGroup     *errgroup.Group
	mu           sync.Mutex
	cleanup      []func() error
	ctx          context.Context
	cancel       func()
}

func (gwCtr *gatewayContainer) Start(ctx context.Context, req client.StartRequest) (client.ContainerProcess, error) {
	resize := make(chan executor.WinSize)
	procInfo := executor.ProcessInfo{
		Meta: executor.Meta{
			Args:         req.Args,
			Env:          req.Env,
			User:         req.User,
			Cwd:          req.Cwd,
			Tty:          req.Tty,
			NetMode:      gwCtr.netMode,
			SecurityMode: gwCtr.securityMode,
		},
		Stdin:  req.Stdin,
		Stdout: req.Stdout,
		Stderr: req.Stderr,
		Resize: resize,
	}
	procInfo.Meta.Env = addDefaultEnvvar(procInfo.Meta.Env, "PATH", utilsystem.DefaultPathEnv)
	if req.Tty {
		procInfo.Meta.Env = addDefaultEnvvar(procInfo.Meta.Env, "TERM", "xterm")
	}

	// mark that we have started on the first call to execProcess for this
	// container, so that future calls will call Exec rather than Run
	gwCtr.mu.Lock()
	started := gwCtr.started
	gwCtr.started = true
	gwCtr.mu.Unlock()

	eg, ctx := errgroup.WithContext(gwCtr.ctx)
	gwProc := &gatewayContainerProcess{
		resize:   resize,
		errGroup: eg,
		groupCtx: ctx,
	}

	if !started {
		startedCh := make(chan struct{})
		gwProc.errGroup.Go(func() error {
			logrus.Debugf("Starting new container for %s with args: %q", gwCtr.id, procInfo.Meta.Args)
			err := gwCtr.executor.Run(ctx, gwCtr.id, gwCtr.rootFS, gwCtr.mounts, procInfo, startedCh)
			return stack.Enable(err)
		})
		select {
		case <-ctx.Done():
		case <-startedCh:
		}
	} else {
		gwProc.errGroup.Go(func() error {
			logrus.Debugf("Execing into container %s with args: %q", gwCtr.id, procInfo.Meta.Args)
			err := gwCtr.executor.Exec(ctx, gwCtr.id, procInfo)
			return stack.Enable(err)
		})
	}

	gwCtr.errGroup.Go(gwProc.errGroup.Wait)

	return gwProc, nil
}

func (gwCtr *gatewayContainer) Release(ctx context.Context) error {
	gwCtr.cancel()
	err1 := gwCtr.errGroup.Wait()

	var err2 error
	for i := len(gwCtr.cleanup) - 1; i >= 0; i-- { // call in LIFO order
		err := gwCtr.cleanup[i]()
		if err2 == nil {
			err2 = err
		}
	}

	if err1 != nil {
		return stack.Enable(err1)
	}
	return stack.Enable(err2)
}

// OnRelease will call the provided function when the Container has been
// released.  The functions are called in LIFO order.
func (gwCtr *gatewayContainer) OnRelease(f func() error) {
	gwCtr.cleanup = append(gwCtr.cleanup, f)
}

type gatewayContainerProcess struct {
	errGroup *errgroup.Group
	groupCtx context.Context
	resize   chan<- executor.WinSize
	mu       sync.Mutex
}

func (gwProc *gatewayContainerProcess) Wait() error {
	err := stack.Enable(gwProc.errGroup.Wait())
	gwProc.mu.Lock()
	defer gwProc.mu.Unlock()
	close(gwProc.resize)
	return err
}

func (gwProc *gatewayContainerProcess) Resize(ctx context.Context, size client.WinSize) error {
	gwProc.mu.Lock()
	defer gwProc.mu.Unlock()

	//  is the container done or should we proceed with sending event?
	select {
	case <-gwProc.groupCtx.Done():
		return nil
	case <-ctx.Done():
		return nil
	default:
	}

	// now we select on contexts again in case p.resize blocks b/c
	// container no longer reading from it.  In that case when
	// the errgroup finishes we want to unblock on the write
	// and exit
	select {
	case <-gwProc.groupCtx.Done():
	case <-ctx.Done():
	case gwProc.resize <- executor.WinSize{Cols: size.Cols, Rows: size.Rows}:
	}
	return nil
}

func addDefaultEnvvar(env []string, k, v string) []string {
	for _, e := range env {
		if strings.HasPrefix(e, k+"=") {
			return env
		}
	}
	return append(env, k+"="+v)
}
