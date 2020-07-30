package containerdexecutor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/containerd/containerd"
	"github.com/containerd/containerd/cio"
	containerdoci "github.com/containerd/containerd/oci"
	"github.com/containerd/continuity/fs"
	"github.com/docker/docker/pkg/idtools"
	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/executor"
	"github.com/moby/buildkit/executor/oci"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/snapshot"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/network"
	"github.com/opencontainers/runtime-spec/specs-go"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

type containerdExecutor struct {
	client           *containerd.Client
	root             string
	networkProviders map[pb.NetMode]network.Provider
	cgroupParent     string
	dnsConfig        *oci.DNSConfig
	running          map[string]chan error
	mu               sync.Mutex
}

// New creates a new executor backed by connection to containerd API
func New(client *containerd.Client, root, cgroup string, networkProviders map[pb.NetMode]network.Provider, dnsConfig *oci.DNSConfig) executor.Executor {
	// clean up old hosts/resolv.conf file. ignore errors
	os.RemoveAll(filepath.Join(root, "hosts"))
	os.RemoveAll(filepath.Join(root, "resolv.conf"))

	return &containerdExecutor{
		client:           client,
		root:             root,
		networkProviders: networkProviders,
		cgroupParent:     cgroup,
		dnsConfig:        dnsConfig,
		running:          make(map[string]chan error),
	}
}

func (w *containerdExecutor) Run(ctx context.Context, id string, root cache.Mountable, mounts []executor.Mount, process executor.ProcessInfo, started chan<- struct{}) (err error) {
	if id == "" {
		id = identity.NewID()
	}

	startedOnce := sync.Once{}
	done := make(chan error, 1)
	w.mu.Lock()
	w.running[id] = done
	w.mu.Unlock()
	defer func() {
		w.mu.Lock()
		delete(w.running, id)
		w.mu.Unlock()
		done <- err
		close(done)
		if started != nil {
			startedOnce.Do(func() {
				close(started)
			})
		}
	}()

	meta := process.Meta

	resolvConf, err := oci.GetResolvConf(ctx, w.root, nil, w.dnsConfig)
	if err != nil {
		return err
	}

	hostsFile, clean, err := oci.GetHostsFile(ctx, w.root, meta.ExtraHosts, nil)
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

	rootMounts, release, err := mountable.Mount()
	if err != nil {
		return err
	}
	if release != nil {
		defer release()
	}

	var sgids []uint32
	uid, gid, err := oci.ParseUIDGID(meta.User)
	if err != nil {
		lm := snapshot.LocalMounterWithMounts(rootMounts)
		rootfsPath, err := lm.Mount()
		if err != nil {
			return err
		}
		uid, gid, sgids, err = oci.GetUser(rootfsPath, meta.User)
		if err != nil {
			lm.Unmount()
			return err
		}

		identity := idtools.Identity{
			UID: int(uid),
			GID: int(gid),
		}

		newp, err := fs.RootPath(rootfsPath, meta.Cwd)
		if err != nil {
			lm.Unmount()
			return errors.Wrapf(err, "working dir %s points to invalid target", newp)
		}
		if _, err := os.Stat(newp); err != nil {
			if err := idtools.MkdirAllAndChown(newp, 0755, identity); err != nil {
				lm.Unmount()
				return errors.Wrapf(err, "failed to create working directory %s", newp)
			}
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
	processMode := oci.ProcessSandbox // FIXME(AkihiroSuda)
	spec, cleanup, err := oci.GenerateSpec(ctx, meta, mounts, id, resolvConf, hostsFile, namespace, processMode, nil, opts...)
	if err != nil {
		return err
	}
	defer cleanup()
	spec.Process.Terminal = meta.Tty

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

	cioOpts := []cio.Opt{cio.WithStreams(process.Stdin, process.Stdout, process.Stderr)}
	if meta.Tty {
		cioOpts = append(cioOpts, cio.WithTerminal)
	}

	task, err := container.NewTask(ctx, cio.NewCreator(cioOpts...), containerd.WithRootFS(rootMounts))
	if err != nil {
		return err
	}

	defer func() {
		if _, err1 := task.Delete(context.TODO()); err == nil && err1 != nil {
			err = errors.Wrapf(err1, "failed to delete task %s", id)
		}
	}()

	err = w.runProcess(ctx, task, process.Resize, func() {
		startedOnce.Do(func() {
			if started != nil {
				close(started)
			}
		})
	})
	return err
}

func (w *containerdExecutor) Exec(ctx context.Context, id string, process executor.ProcessInfo) (err error) {
	meta := process.Meta

	// first verify the container is running, if we get an error assume the container
	// is in the process of being created and check again every 100ms or until
	// context is canceled.

	var container containerd.Container
	var task containerd.Task
	for {
		w.mu.Lock()
		done, ok := w.running[id]
		w.mu.Unlock()

		if !ok {
			return errors.Errorf("container %s not found", id)
		}

		if container == nil {
			container, _ = w.client.LoadContainer(ctx, id)
		}
		if container != nil && task == nil {
			task, _ = container.Task(ctx, nil)
		}
		if task != nil {
			status, _ := task.Status(ctx)
			if status.Status == containerd.Running {
				break
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err, ok := <-done:
			if !ok || err == nil {
				return errors.Errorf("container %s has stopped", id)
			}
			return errors.Wrapf(err, "container %s has exited with error", id)
		case <-time.After(100 * time.Millisecond):
			continue
		}
	}

	spec, err := container.Spec(ctx)
	if err != nil {
		return errors.WithStack(err)
	}

	proc := spec.Process

	// TODO how do we get rootfsPath for oci.GetUser in case user passed in username rather than uid:gid?
	// For now only support uid:gid
	if meta.User != "" {
		uid, gid, err := oci.ParseUIDGID(meta.User)
		if err != nil {
			return errors.WithStack(err)
		}
		proc.User = specs.User{
			UID:            uid,
			GID:            gid,
			AdditionalGids: []uint32{},
		}
	}

	proc.Terminal = meta.Tty
	proc.Args = meta.Args
	if meta.Cwd != "" {
		spec.Process.Cwd = meta.Cwd
	}
	if len(process.Meta.Env) > 0 {
		spec.Process.Env = process.Meta.Env
	}

	cioOpts := []cio.Opt{cio.WithStreams(process.Stdin, process.Stdout, process.Stderr)}
	if meta.Tty {
		cioOpts = append(cioOpts, cio.WithTerminal)
	}

	taskProcess, err := task.Exec(ctx, identity.NewID(), proc, cio.NewCreator(cioOpts...))
	if err != nil {
		return errors.WithStack(err)
	}

	return w.runProcess(ctx, taskProcess, process.Resize, nil)
}

func (w *containerdExecutor) runProcess(ctx context.Context, p containerd.Process, resize <-chan executor.WinSize, started func()) (err error) {
	err = p.Start(ctx)
	if err != nil {
		return err
	}

	if started != nil {
		started()
	}

	statusCh, err := p.Wait(context.Background())
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
			p.Kill(killCtx, syscall.SIGKILL)
		case size := <-resize:
			err := p.Resize(ctx, size.Cols, size.Rows)
			if err != nil {
				cancel()
				return err
			}
		case status := <-statusCh:
			if cancel != nil {
				cancel()
			}
			if status.ExitCode() != 0 {
				var err error
				if status.ExitCode() == containerd.UnknownExitStatus && status.Error() != nil {
					err = errors.Wrap(status.Error(), "failure waiting for process")
				} else {
					err = &executor.ExitError{
						ExitCode: status.ExitCode(),
						Err:      status.Error(),
					}
				}
				select {
				case <-ctx.Done():
					err = errors.Wrap(ctx.Err(), err.Error())
				default:
				}
				return err
			}
			return nil
		}
	}
}
