package containerd

import (
	"context"
	"time"

	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/errdefs"
	"github.com/containerd/containerd/namespaces"
	"github.com/opencontainers/go-digest"
)

type garbageCollectFn func(context.Context) error

func NewContentStore(store content.Store, ns string, gc func(context.Context) error) content.Store {
	return &noGCContentStore{&nsContent{ns, store, gc}}
}

type nsContent struct {
	ns string
	content.Store
	gc garbageCollectFn
}

func (c *nsContent) Info(ctx context.Context, dgst digest.Digest) (content.Info, error) {
	ctx = namespaces.WithNamespace(ctx, c.ns)
	return c.Store.Info(ctx, dgst)
}

func (c *nsContent) Update(ctx context.Context, info content.Info, fieldpaths ...string) (content.Info, error) {
	ctx = namespaces.WithNamespace(ctx, c.ns)
	return c.Store.Update(ctx, info, fieldpaths...)
}

func (c *nsContent) Walk(ctx context.Context, fn content.WalkFunc, filters ...string) error {
	ctx = namespaces.WithNamespace(ctx, c.ns)
	return c.Store.Walk(ctx, fn, filters...)
}

func (c *nsContent) Delete(ctx context.Context, dgst digest.Digest) error {
	ctx = namespaces.WithNamespace(ctx, c.ns)
	if _, err := c.Update(ctx, content.Info{
		Digest: dgst,
	}, "labels.containerd.io/gc.root"); err != nil {
		return err
	} // calling snapshotter.Remove here causes a race in containerd
	if c.gc == nil {
		return nil
	}
	return c.gc(ctx)
}

func (c *nsContent) Status(ctx context.Context, ref string) (content.Status, error) {
	ctx = namespaces.WithNamespace(ctx, c.ns)
	return c.Store.Status(ctx, ref)
}

func (c *nsContent) ListStatuses(ctx context.Context, filters ...string) ([]content.Status, error) {
	ctx = namespaces.WithNamespace(ctx, c.ns)
	return c.Store.ListStatuses(ctx, filters...)
}

func (c *nsContent) Abort(ctx context.Context, ref string) error {
	ctx = namespaces.WithNamespace(ctx, c.ns)
	return c.Store.Abort(ctx, ref)
}

func (c *nsContent) ReaderAt(ctx context.Context, dgst digest.Digest) (content.ReaderAt, error) {
	ctx = namespaces.WithNamespace(ctx, c.ns)
	return c.Store.ReaderAt(ctx, dgst)
}

func (c *nsContent) Writer(ctx context.Context, ref string, size int64, expected digest.Digest) (content.Writer, error) {
	return c.writer(ctx, ref, size, expected, 3)
}

func (c *nsContent) writer(ctx context.Context, ref string, size int64, expected digest.Digest, retries int) (content.Writer, error) {
	ctx = namespaces.WithNamespace(ctx, c.ns)
	w, err := c.Store.Writer(ctx, ref, size, expected)
	if err != nil {
		if errdefs.IsAlreadyExists(err) && expected != "" && retries > 0 {
			_, err2 := c.Update(ctx, content.Info{
				Digest: expected,
				Labels: map[string]string{
					"containerd.io/gc.root": time.Now().UTC().Format(time.RFC3339Nano),
				},
			}, "labels.containerd.io/gc.root")
			if err2 != nil {
				return c.writer(ctx, ref, size, expected, retries-1)
			}
		}
	}
	return w, err
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
