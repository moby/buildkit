package containerdworker

import (
	"io"

	"github.com/containerd/containerd"
	"github.com/containerd/containerd/cio"
	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/worker"
	"github.com/moby/buildkit/worker/bridge"
	"github.com/moby/buildkit/worker/oci"
	"github.com/pkg/errors"
	"golang.org/x/net/context"
)

type containerdWorker struct {
	client *containerd.Client
}

func New(client *containerd.Client) worker.Worker {
	return containerdWorker{
		client: client,
	}
}

func (w containerdWorker) Exec(ctx context.Context, meta worker.Meta, root cache.Mountable, mounts []worker.Mount, stdin io.ReadCloser, stdout, stderr io.WriteCloser) error {
	id := identity.NewID()

	spec, cleanup, err := oci.GenerateSpec(ctx, meta, mounts, id)
	if err != nil {
		return err
	}
	defer cleanup()

	rootMounts, err := root.Mount(ctx, false)
	if err != nil {
		return err
	}

	container, err := w.client.NewContainer(ctx, id,
		containerd.WithSpec(spec),
	)
	if err != nil {
		return err
	}
	defer container.Delete(ctx)

	if stdin == nil {
		stdin = &emptyReadCloser{}
	}

	task, err := container.NewTask(ctx, cio.NewIO(stdin, stdout, stderr), containerd.WithRootFS(rootMounts))
	if err != nil {
		return err
	}
	defer task.Delete(ctx)

	// FIXME: remove hardcoded "docker0" with user input
	pair, err := bridge.CreateBridgePair("docker0")
	if err != nil {
		return errors.Errorf("error in paring : %v", err)
	}

	if err := pair.Set(int(task.Pid())); err != nil {
		return errors.Errorf("could not set bridge network : %v", err)
	}
	defer pair.Remove()
	// TODO: support sending signals

	if err := task.Start(ctx); err != nil {
		return err
	}

	statusCh, err := task.Wait(ctx)
	if err != nil {
		return err
	}
	status := <-statusCh
	if status.ExitCode() != 0 {
		return errors.Errorf("process returned non-zero exit code: %d", status.ExitCode())
	}

	return nil
}

type emptyReadCloser struct{}

func (*emptyReadCloser) Read([]byte) (int, error) {
	return 0, io.EOF
}

func (*emptyReadCloser) Close() error {
	return nil
}
