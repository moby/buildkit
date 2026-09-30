package forwarder

import (
	"context"
	"runtime"
	"testing"

	"github.com/moby/buildkit/cache"
	buildkitclient "github.com/moby/buildkit/client"
	"github.com/moby/buildkit/executor"
	resourcestypes "github.com/moby/buildkit/executor/resources/types"
	gwclient "github.com/moby/buildkit/frontend/gateway/client"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/system"
	"github.com/stretchr/testify/require"
)

func TestNewContainerPreservesPlatform(t *testing.T) {
	exec := &recordingExecutor{process: make(chan executor.ProcessInfo, 1)}
	c := &BridgeClient{
		workers:  emptyWorkerInfos{},
		executor: exec,
	}
	targetOS := "windows"
	if runtime.GOOS == targetOS {
		targetOS = "linux"
	}
	ctx := t.Context()

	ctr, err := c.NewContainer(ctx, gwclient.NewContainerRequest{
		Platform: &pb.Platform{OS: targetOS, Architecture: "amd64"},
	})
	require.NoError(t, err)

	proc, err := ctr.Start(ctx, gwclient.StartRequest{})
	require.NoError(t, err)
	require.Contains(t, (<-exec.process).Meta.Env, "PATH="+system.DefaultPathEnv(targetOS))
	require.NoError(t, proc.Wait())
	require.NoError(t, ctr.Release(ctx))
}

type emptyWorkerInfos struct{}

func (emptyWorkerInfos) DefaultCacheManager() (cache.Manager, error) {
	return nil, nil
}

func (emptyWorkerInfos) WorkerInfos() []buildkitclient.WorkerInfo {
	return nil
}

type recordingExecutor struct {
	process chan executor.ProcessInfo
}

func (e *recordingExecutor) Run(_ context.Context, _ string, _ executor.Mount, _ []executor.Mount, process executor.ProcessInfo, started chan<- struct{}) (resourcestypes.Recorder, error) {
	e.process <- process
	close(started)
	return nil, nil
}

func (e *recordingExecutor) Exec(context.Context, string, executor.ProcessInfo) error {
	return nil
}
