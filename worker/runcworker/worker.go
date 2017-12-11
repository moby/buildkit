package runcworker

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"github.com/containerd/containerd/mount"
	runc "github.com/containerd/go-runc"
	"github.com/docker/docker/pkg/symlink"
	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/worker"
	"github.com/moby/buildkit/worker/oci"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"golang.org/x/net/context"
)

type runcworker struct {
	runc *runc.Runc
	root string
}

func New(root string) (worker.Worker, error) {
	if err := exec.Command("runc", "--version").Run(); err != nil {
		return nil, errors.Wrap(err, "failed to find runc binary")
	}

	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, errors.Wrapf(err, "failed to create %s", root)
	}

	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	// TODO: check that root is not symlink to fail early

	runtime := &runc.Runc{
		Log:          filepath.Join(root, "runc-log.json"),
		LogFormat:    runc.JSON,
		PdeathSignal: syscall.SIGKILL,
		Setpgid:      true,
	}

	w := &runcworker{
		runc: runtime,
		root: root,
	}
	return w, nil
}

func (w *runcworker) Exec(ctx context.Context, meta worker.Meta, root cache.Mountable, mounts []worker.Mount, stdin io.ReadCloser, stdout, stderr io.WriteCloser) error {

	resolvConf, err := oci.GetResolvConf(ctx, w.root)
	if err != nil {
		return err
	}

	hostsFile, err := oci.GetHostsFile(ctx, w.root)
	if err != nil {
		return err
	}

	rootMount, err := root.Mount(ctx, false)
	if err != nil {
		return err
	}

	id := identity.NewID()
	bundle := filepath.Join(w.root, id)

	if err := os.Mkdir(bundle, 0700); err != nil {
		return err
	}
	defer os.RemoveAll(bundle)
	rootFSPath := filepath.Join(bundle, "rootfs")
	if err := os.Mkdir(rootFSPath, 0700); err != nil {
		return err
	}
	f, err := os.Create(filepath.Join(bundle, "config.json"))
	if err != nil {
		return err
	}
	defer f.Close()
	spec, cleanup, err := oci.GenerateSpec(ctx, meta, mounts, id, resolvConf, hostsFile)
	if err != nil {
		return err
	}
	defer cleanup()

	if err := mount.All(rootMount, rootFSPath); err != nil {
		return err
	}
	defer mount.Unmount(rootFSPath, 0)
	spec.Root.Path = rootFSPath
	if _, ok := root.(cache.ImmutableRef); ok { // TODO: pass in with mount, not ref type
		spec.Root.Readonly = true
	}

	newp, err := symlink.FollowSymlinkInScope(filepath.Join(rootFSPath, meta.Cwd), rootFSPath)
	if err != nil {
		return errors.Wrapf(err, "working dir %s points to invalid target", newp)
	}
	if err := os.MkdirAll(newp, 0700); err != nil {
		return errors.Wrapf(err, "failed to create working directory %s", newp)
	}

	if err := json.NewEncoder(f).Encode(spec); err != nil {
		return err
	}

	logrus.Debugf("> running %s %v", id, meta.Args)

	status, err := w.runc.Run(ctx, id, bundle, &runc.CreateOpts{
		IO: &forwardIO{stdin: stdin, stdout: stdout, stderr: stderr},
	})
	logrus.Debugf("< completed %s %v %v", id, status, err)
	if status != 0 {
		select {
		case <-ctx.Done():
			// runc can't report context.Cancelled directly
			return errors.Wrapf(ctx.Err(), "exit code %d", status)
		default:
		}
		return errors.Errorf("exit code %d", status)
	}

	return err
}

type forwardIO struct {
	stdin          io.ReadCloser
	stdout, stderr io.WriteCloser
}

func (s *forwardIO) Close() error {
	return nil
}

func (s *forwardIO) Set(cmd *exec.Cmd) {
	cmd.Stdin = s.stdin
	cmd.Stdout = s.stdout
	cmd.Stderr = s.stderr
}

func (s *forwardIO) Stdin() io.WriteCloser {
	return nil
}

func (s *forwardIO) Stdout() io.ReadCloser {
	return nil
}

func (s *forwardIO) Stderr() io.ReadCloser {
	return nil
}
