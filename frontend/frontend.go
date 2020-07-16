package frontend

import (
	"context"

	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/executor"
	gw "github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/solver/pb"
	digest "github.com/opencontainers/go-digest"
)

type Frontend interface {
	Solve(ctx context.Context, llb FrontendLLBBridge, opt map[string]string, inputs map[string]*pb.Definition, sid string) (*Result, error)
}

type FrontendLLBBridge interface {
	Solve(ctx context.Context, req SolveRequest, sid string) (*Result, error)
	ResolveImageConfig(ctx context.Context, ref string, opt llb.ResolveImageConfigOpt) (digest.Digest, []byte, error)
	// Run will start a container for the given process with rootfs, mounts.
	// `id` is an optional name for the container so it can be referenced later via Exec.
	// `started` is an optional channel that will be closed when the container setup completes and has started running.
	Run(ctx context.Context, id string, rootfs cache.Mountable, mounts []executor.Mount, process executor.ProcessInfo, started chan<- struct{}) error
	// Exec will start a process in container matching `id`. An error will be returned
	// if the container failed to start (via Run) or has exited before Exec is called.
	Exec(ctx context.Context, id string, process executor.ProcessInfo) error
}

type SolveRequest = gw.SolveRequest

type CacheOptionsEntry = gw.CacheOptionsEntry

type WorkerInfos interface {
	WorkerInfos() []client.WorkerInfo
}
