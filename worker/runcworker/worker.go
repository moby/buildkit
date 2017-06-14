package runcworker

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"github.com/Sirupsen/logrus"
	"github.com/containerd/containerd/mount"
	runc "github.com/containerd/go-runc"
	"github.com/pkg/errors"
	"github.com/tonistiigi/buildkit_poc/cache"
	"github.com/tonistiigi/buildkit_poc/worker"
	"github.com/tonistiigi/buildkit_poc/worker/oci"
	"golang.org/x/net/context"
)

type runcworker struct {
	runc *runc.Runc
	root string
}

func New(root string) (worker.Worker, error) {
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
	}

	w := &runcworker{
		runc: runtime,
		root: root,
	}
	return w, nil
}

func (w *runcworker) Exec(ctx context.Context, meta worker.Meta, mounts map[string]cache.Mountable, stdout, stderr io.WriteCloser) error {
	root, ok := mounts["/"]
	if !ok {
		return errors.Errorf("no root mount")
	}

	rootMount, err := root.Mount(ctx)
	if err != nil {
		return err
	}

	id := generateID()
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

	spec, err := oci.GenerateSpec(ctx, meta, mounts)
	if err != nil {
		return err
	}

	if err := mount.MountAll(rootMount, rootFSPath); err != nil {
		return err
	}
	defer mount.Unmount(rootFSPath, 0)
	spec.Root.Path = rootFSPath
	if _, ok := root.(cache.ImmutableRef); ok {
		spec.Root.Readonly = true
	}

	if err := json.NewEncoder(f).Encode(spec); err != nil {
		return err
	}

	logrus.Debugf("> running %s %v", id, meta.Args)

	status, err := w.runc.Run(ctx, id, bundle, &runc.CreateOpts{
		IO: &forwardIO{stdout: stdout, stderr: stderr},
	})
	logrus.Debugf("< completed %s %v %v", id, status, err)
	if status != 0 {
		return errors.Errorf("exit code %d", status)
	}

	return err
}

type forwardIO struct {
	stdout, stderr io.WriteCloser
}

func (s *forwardIO) Close() error {
	return nil
}

func (s *forwardIO) Set(cmd *exec.Cmd) {
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

func generateID() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
