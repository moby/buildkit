package containerdworker

import (
	"io"
	"syscall"
	"time"

	"github.com/containerd/containerd"
	"github.com/containerd/containerd/cio"
	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/worker"
	"github.com/moby/buildkit/worker/oci"
	"github.com/pkg/errors"
	"golang.org/x/net/context"
)

type containerdWorker struct {
	client *containerd.Client
	root   string
}

func New(client *containerd.Client, root string) worker.Worker {
	return containerdWorker{
		client: client,
		root:   root,
	}
}

func (w containerdWorker) Exec(ctx context.Context, meta worker.Meta, root cache.Mountable, mounts []worker.Mount, stdin io.ReadCloser, stdout, stderr io.WriteCloser) (err error) {
	id := identity.NewID()

	resolvConf, err := oci.GetResolvConf(ctx, w.root)
	if err != nil {
		return err
	}

	hostsFile, err := oci.GetHostsFile(ctx, w.root)
	if err != nil {
		return err
	}

	spec, cleanup, err := oci.GenerateSpec(ctx, meta, mounts, id, resolvConf, hostsFile)
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

	defer func() {
		if err1 := container.Delete(context.TODO()); err == nil && err1 != nil {
			err = errors.Wrapf(err1, "failed to delete container %s", id)
		}
	}()

	if stdin == nil {
		stdin = &emptyReadCloser{}
	}

	task, err := container.NewTask(ctx, cio.NewIO(stdin, stdout, stderr), containerd.WithRootFS(rootMounts))
	if err != nil {
		return err
	}
	defer func() {
		if _, err1 := task.Delete(context.TODO()); err == nil && err1 != nil {
			err = errors.Wrapf(err1, "failed to delete task %s", id)
		}
	}()

	// TODO: Configure bridge networking

	if err := task.Start(ctx); err != nil {
		return err
	}

	statusCh, err := task.Wait(context.Background())
	if err != nil {
		return err
	}

	killCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)

	ctxDone := ctx.Done()
	for {
		select {
		case <-ctxDone:
			ctxDone = nil
			task.Kill(killCtx, syscall.SIGKILL)
		case status := <-statusCh:
			cancel()
			if status.ExitCode() != 0 {
				return errors.Errorf("process returned non-zero exit code: %d", status.ExitCode())
			}
			return nil
		}
	}

}

type emptyReadCloser struct{}

func (*emptyReadCloser) Read([]byte) (int, error) {
	return 0, io.EOF
}

func (*emptyReadCloser) Close() error {
	return nil
}
