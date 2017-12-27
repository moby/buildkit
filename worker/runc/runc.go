package runc

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/boltdb/bolt"
	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/content/local"
	"github.com/containerd/containerd/diff/walking"
	ctdmetadata "github.com/containerd/containerd/metadata"
	"github.com/containerd/containerd/mount"
	"github.com/containerd/containerd/namespaces"
	ctdsnapshot "github.com/containerd/containerd/snapshots"
	"github.com/containerd/containerd/snapshots/overlay"
	"github.com/moby/buildkit/cache/metadata"
	"github.com/moby/buildkit/executor/runcexecutor"
	"github.com/moby/buildkit/worker/base"
	"github.com/opencontainers/go-digest"
	"github.com/sirupsen/logrus"
)

// NewWorkerOpt creates a WorkerOpt.
// But it does not set the following fields:
//  - SessionManager
func NewWorkerOpt(root string, labels map[string]string) (base.WorkerOpt, error) {
	var opt base.WorkerOpt
	name := "runc-overlayfs"
	root = filepath.Join(root, name)
	if err := os.MkdirAll(root, 0700); err != nil {
		return opt, err
	}
	md, err := metadata.NewStore(filepath.Join(root, "metadata.db"))
	if err != nil {
		return opt, err
	}
	exe, err := runcexecutor.New(filepath.Join(root, "executor"))
	if err != nil {
		return opt, err
	}
	s, err := overlay.NewSnapshotter(filepath.Join(root, "snapshots"))
	if err != nil {
		return opt, err
	}

	c, err := local.NewStore(filepath.Join(root, "content"))
	if err != nil {
		return opt, err
	}

	db, err := bolt.Open(filepath.Join(root, "containerdmeta.db"), 0644, nil)
	if err != nil {
		return opt, err
	}

	mdb := ctdmetadata.NewDB(db, c, map[string]ctdsnapshot.Snapshotter{
		"overlayfs": s,
	})
	if err := mdb.Init(context.TODO()); err != nil {
		return opt, err
	}

	c = &noGCContentStore{&nsContent{mdb.ContentStore(), mdb.GarbageCollect}}
	df, err := walking.NewWalkingDiff(c)
	if err != nil {
		return opt, err
	}

	// TODO: call mdb.GarbageCollect . maybe just inject it into nsSnapshotter.Remove and csContent.Delete

	id, err := base.ID(root)
	if err != nil {
		return opt, err
	}
	xlabels := base.Labels("oci", "overlayfs")
	for k, v := range labels {
		xlabels[k] = v
	}
	opt = base.WorkerOpt{
		ID:              id,
		Labels:          xlabels,
		MetadataStore:   md,
		Executor:        exe,
		BaseSnapshotter: &nsSnapshotter{mdb.Snapshotter("overlayfs"), mdb.GarbageCollect},
		ContentStore:    c,
		Applier:         df,
		Differ:          df,
		ImageStore:      nil, // explicitly
	}
	return opt, nil
}

// this should be supported by containerd. currently packages are unusable without wrapping
const dummyNs = "buildkit"

type garbageCollect func(context.Context) (ctdmetadata.GCStats, error)

type nsContent struct {
	content.Store
	gc garbageCollect
}

func (c *nsContent) Info(ctx context.Context, dgst digest.Digest) (content.Info, error) {
	ctx = namespaces.WithNamespace(ctx, dummyNs)
	return c.Store.Info(ctx, dgst)
}

func (c *nsContent) Update(ctx context.Context, info content.Info, fieldpaths ...string) (content.Info, error) {
	ctx = namespaces.WithNamespace(ctx, dummyNs)
	return c.Store.Update(ctx, info, fieldpaths...)
}

func (c *nsContent) Walk(ctx context.Context, fn content.WalkFunc, filters ...string) error {
	ctx = namespaces.WithNamespace(ctx, dummyNs)
	return c.Store.Walk(ctx, fn, filters...)
}

func (c *nsContent) Delete(ctx context.Context, dgst digest.Digest) error {
	ctx = namespaces.WithNamespace(ctx, dummyNs)
	logrus.Debugf("delete-blob", dgst)
	if err := c.Store.Delete(ctx, dgst); err != nil {
		return err
	}
	_, err := c.gc(ctx)
	return err
}

func (c *nsContent) Status(ctx context.Context, ref string) (content.Status, error) {
	ctx = namespaces.WithNamespace(ctx, dummyNs)
	return c.Store.Status(ctx, ref)
}

func (c *nsContent) ListStatuses(ctx context.Context, filters ...string) ([]content.Status, error) {
	ctx = namespaces.WithNamespace(ctx, dummyNs)
	return c.Store.ListStatuses(ctx, filters...)
}

func (c *nsContent) Abort(ctx context.Context, ref string) error {
	ctx = namespaces.WithNamespace(ctx, dummyNs)
	return c.Store.Abort(ctx, ref)
}

func (c *nsContent) ReaderAt(ctx context.Context, dgst digest.Digest) (content.ReaderAt, error) {
	ctx = namespaces.WithNamespace(ctx, dummyNs)
	return c.Store.ReaderAt(ctx, dgst)
}

func (c *nsContent) Writer(ctx context.Context, ref string, size int64, expected digest.Digest) (content.Writer, error) {
	ctx = namespaces.WithNamespace(ctx, dummyNs)
	return c.Store.Writer(ctx, ref, size, expected)
}

type nsSnapshotter struct {
	ctdsnapshot.Snapshotter
	gc garbageCollect
}

func (s *nsSnapshotter) Stat(ctx context.Context, key string) (ctdsnapshot.Info, error) {
	ctx = namespaces.WithNamespace(ctx, dummyNs)
	return s.Snapshotter.Stat(ctx, key)
}

func (s *nsSnapshotter) Update(ctx context.Context, info ctdsnapshot.Info, fieldpaths ...string) (ctdsnapshot.Info, error) {
	ctx = namespaces.WithNamespace(ctx, dummyNs)
	return s.Snapshotter.Update(ctx, info, fieldpaths...)
}

func (s *nsSnapshotter) Usage(ctx context.Context, key string) (ctdsnapshot.Usage, error) {
	ctx = namespaces.WithNamespace(ctx, dummyNs)
	return s.Snapshotter.Usage(ctx, key)
}
func (s *nsSnapshotter) Mounts(ctx context.Context, key string) ([]mount.Mount, error) {
	ctx = namespaces.WithNamespace(ctx, dummyNs)
	return s.Snapshotter.Mounts(ctx, key)
}
func (s *nsSnapshotter) Prepare(ctx context.Context, key, parent string, opts ...ctdsnapshot.Opt) ([]mount.Mount, error) {
	ctx = namespaces.WithNamespace(ctx, dummyNs)
	return s.Snapshotter.Prepare(ctx, key, parent, opts...)
}
func (s *nsSnapshotter) View(ctx context.Context, key, parent string, opts ...ctdsnapshot.Opt) ([]mount.Mount, error) {
	ctx = namespaces.WithNamespace(ctx, dummyNs)
	return s.Snapshotter.View(ctx, key, parent, opts...)
}
func (s *nsSnapshotter) Commit(ctx context.Context, name, key string, opts ...ctdsnapshot.Opt) error {
	ctx = namespaces.WithNamespace(ctx, dummyNs)
	return s.Snapshotter.Commit(ctx, name, key, opts...)
}
func (s *nsSnapshotter) Remove(ctx context.Context, key string) error {
	ctx = namespaces.WithNamespace(ctx, dummyNs)
	if _, err := s.Update(ctx, ctdsnapshot.Info{
		Name: key,
	}, "labels.containerd.io/gc.root"); err != nil {
		return err
	} // calling snapshotter.Remove here causes a race in containerd
	_, err := s.gc(ctx)
	return err
}
func (s *nsSnapshotter) Walk(ctx context.Context, fn func(context.Context, ctdsnapshot.Info) error) error {
	ctx = namespaces.WithNamespace(ctx, dummyNs)
	return s.Snapshotter.Walk(ctx, fn)
}

type noGCContentStore struct {
	content.Store
}
type noGCWriter struct {
	content.Writer
}

func (cs *noGCContentStore) Writer(ctx context.Context, ref string, size int64, expected digest.Digest) (content.Writer, error) {
	w, err := cs.Store.Writer(ctx, ref, size, expected)
	return &noGCWriter{w}, err
}

func (w *noGCWriter) Commit(ctx context.Context, size int64, expected digest.Digest, opts ...content.Opt) error {
	opts = append(opts, func(info *content.Info) error {
		if info.Labels == nil {
			info.Labels = map[string]string{}
		}
		info.Labels["containerd.io/gc.root"] = time.Now().UTC().Format(time.RFC3339Nano)
		return nil
	})
	return w.Writer.Commit(ctx, size, expected, opts...)
}
