package containerdworker

import (
	"io"

	"github.com/containerd/containerd"
	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/worker"
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

func (w containerdWorker) Exec(ctx context.Context, meta worker.Meta, root cache.Mountable, mounts []worker.Mount, stdout, stderr io.WriteCloser) error {
	id := identity.NewID()

	spec, err := oci.GenerateSpec(ctx, meta, mounts)
	if err != nil {
		return err
	}

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

	task, err := container.NewTask(ctx, containerd.Stdio, containerd.WithRootFS(rootMounts))
	if err != nil {
		return err
	}
	defer task.Delete(ctx)

	// TODO: Configure bridge networking

	// TODO: support sending signals

	if err := task.Start(ctx); err != nil {
		return err
	}

	status, err := task.Wait(ctx)
	if err != nil {
		return err
	}
	if status != 0 {
		return errors.Errorf("process returned non-zero status: %d", status)
	}

	return nil
}
