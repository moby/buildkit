package containerdexecutor

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/containerd/containerd"
	"github.com/containerd/containerd/cio"
	"github.com/containerd/containerd/contrib/seccomp"
	containerdoci "github.com/containerd/containerd/oci"
	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/executor"
	"github.com/moby/buildkit/executor/oci"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/snapshot"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/network"
	"github.com/moby/buildkit/util/system"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

type containerdExecutor struct {
	client           *containerd.Client
	root             string
	networkProviders map[pb.NetMode]network.Provider
	cgroupParent     string
}

func New(client *containerd.Client, root, cgroup string, networkProviders map[pb.NetMode]network.Provider) executor.Executor {
	return containerdExecutor{
		client:           client,
		root:             root,
		networkProviders: networkProviders,
		cgroupParent:     cgroup,
	}
}

func (w containerdExecutor) Exec(ctx context.Context, meta executor.Meta, root cache.Mountable, mounts []executor.Mount, stdin io.ReadCloser, stdout, stderr io.WriteCloser) (err error) {
	id := identity.NewID()

	resolvConf, err := oci.GetResolvConf(ctx, w.root)
	if err != nil {
		return err
	}

	hostsFile, clean, err := oci.GetHostsFile(ctx, w.root, meta.ExtraHosts)
	if err != nil {
		return err
	}
	if clean != nil {
		defer clean()
	}

	mountable, err := root.Mount(ctx, false)
	if err != nil {
		return err
	}

	rootMounts, err := mountable.Mount()
	if err != nil {
		return err
	}
	defer mountable.Release()

	var sgids []uint32
	uid, gid, err := oci.ParseUIDGID(meta.User)
	if err != nil {
		lm := snapshot.LocalMounterWithMounts(rootMounts)
		rootfsPath, err := lm.Mount()
		if err != nil {
			return err
		}
		uid, gid, sgids, err = oci.GetUser(ctx, rootfsPath, meta.User)
		if err != nil {
			lm.Unmount()
			return err
		}
		lm.Unmount()
	}

	provider, ok := w.networkProviders[meta.NetMode]
	if !ok {
		return errors.Errorf("unknown network mode %s", meta.NetMode)
	}
	namespace, err := provider.New()
	if err != nil {
		return err
	}
	defer namespace.Close()

	if meta.NetMode == pb.NetMode_HOST {
		logrus.Info("enabling HostNetworking")
	}

	opts := []containerdoci.SpecOpts{oci.WithUIDGID(uid, gid, sgids)}
	if meta.ReadonlyRootFS {
		opts = append(opts, containerdoci.WithRootFSReadonly())
	}
	if system.SeccompSupported() {
		opts = append(opts, seccomp.WithDefaultProfile())
	}
	if w.cgroupParent != "" {
		var cgroupsPath string
		lastSeparator := w.cgroupParent[len(w.cgroupParent)-1:]
		if strings.Contains(w.cgroupParent, ".slice") && lastSeparator == ":" {
			cgroupsPath = w.cgroupParent + id
		} else {
			cgroupsPath = filepath.Join("/", w.cgroupParent, "buildkit", id)
		}
		opts = append(opts, containerdoci.WithCgroup(cgroupsPath))
	}
	spec, cleanup, err := oci.GenerateSpec(ctx, meta, mounts, id, resolvConf, hostsFile, namespace, opts...)
	if err != nil {
		return err
	}
	defer cleanup()

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

	task, err := container.NewTask(ctx, cio.NewCreator(cio.WithStreams(stdin, stdout, stderr)), containerd.WithRootFS(rootMounts))
	if err != nil {
		return err
	}
	defer func() {
		if _, err1 := task.Delete(context.TODO()); err == nil && err1 != nil {
			err = errors.Wrapf(err1, "failed to delete task %s", id)
		}
	}()

	if err := task.Start(ctx); err != nil {
		return err
	}

	statusCh, err := task.Wait(context.Background())
	if err != nil {
		return err
	}

	var cancel func()
	ctxDone := ctx.Done()
	for {
		select {
		case <-ctxDone:
			ctxDone = nil
			var killCtx context.Context
			killCtx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
			task.Kill(killCtx, syscall.SIGKILL)
		case status := <-statusCh:
			if cancel != nil {
				cancel()
			}
			if status.ExitCode() != 0 {
				return errors.Errorf("process returned non-zero exit code: %d", status.ExitCode())
			}
			return nil
		}
	}

}
