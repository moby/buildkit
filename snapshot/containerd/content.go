package containerd

import (
	"context"

	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/namespaces"
	"github.com/containerd/nydus-snapshotter/pkg/errdefs"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
)

type Store = nsContent

func NewContentStore(store content.Store, ns string) *Store {
	return &nsContent{ns, store}
}

type nsContent struct {
	ns string
	content.Store
}

func (c *nsContent) Namespace() string {
	return c.ns
}

func (c *nsContent) WithNamespace(ns string) *Store {
	return NewContentStore(c.Store, ns)
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
	return errors.Errorf("contentstore.Delete usage is forbidden")
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

func (c *nsContent) ReaderAt(ctx context.Context, desc ocispecs.Descriptor) (content.ReaderAt, error) {
	ctx = namespaces.WithNamespace(ctx, c.ns)
	return c.Store.ReaderAt(ctx, desc)
}

func (c *nsContent) Writer(ctx context.Context, opts ...content.WriterOpt) (content.Writer, error) {
	return c.writer(ctx, 3, opts...)
}

func (c *nsContent) WithFallbackNS(ns string) content.Store {
	return &nsFallbackStore{
		main: c,
		fb:   c.WithNamespace(ns),
	}
}

func (c *nsContent) writer(ctx context.Context, retries int, opts ...content.WriterOpt) (content.Writer, error) {
	ctx = namespaces.WithNamespace(ctx, c.ns)
	w, err := c.Store.Writer(ctx, opts...)
	if err != nil {
		return nil, err
	}
	return &nsWriter{Writer: w, ns: c.ns}, nil
}

type nsWriter struct {
	content.Writer
	ns string
}

func (w *nsWriter) Commit(ctx context.Context, size int64, expected digest.Digest, opts ...content.Opt) error {
	ctx = namespaces.WithNamespace(ctx, w.ns)
	return w.Writer.Commit(ctx, size, expected, opts...)
}

type nsFallbackStore struct {
	main *nsContent
	fb   *nsContent
}

var _ content.Store = &nsFallbackStore{}

func (c *nsFallbackStore) Info(ctx context.Context, dgst digest.Digest) (content.Info, error) {
	info, err := c.main.Info(ctx, dgst)
	if err != nil {
		if errdefs.IsNotFound(err) {
			return c.fb.Info(ctx, dgst)
		}
	}
	return info, err
}

func (c *nsFallbackStore) Update(ctx context.Context, info content.Info, fieldpaths ...string) (content.Info, error) {
	return c.main.Update(ctx, info, fieldpaths...)
}

func (c *nsFallbackStore) Walk(ctx context.Context, fn content.WalkFunc, filters ...string) error {
	return c.main.Walk(ctx, fn, filters...)
}

func (c *nsFallbackStore) Delete(ctx context.Context, dgst digest.Digest) error {
	return c.main.Delete(ctx, dgst)
}

func (c *nsFallbackStore) Status(ctx context.Context, ref string) (content.Status, error) {
	return c.main.Status(ctx, ref)
}

func (c *nsFallbackStore) ListStatuses(ctx context.Context, filters ...string) ([]content.Status, error) {
	return c.main.ListStatuses(ctx, filters...)
}

func (c *nsFallbackStore) Abort(ctx context.Context, ref string) error {
	return c.main.Abort(ctx, ref)
}

func (c *nsFallbackStore) ReaderAt(ctx context.Context, desc ocispecs.Descriptor) (content.ReaderAt, error) {
	ra, err := c.main.ReaderAt(ctx, desc)
	if err != nil {
		if errdefs.IsNotFound(err) {
			return c.fb.ReaderAt(ctx, desc)
		}
	}
	return ra, err
}

func (c *nsFallbackStore) Writer(ctx context.Context, opts ...content.WriterOpt) (content.Writer, error) {
	return c.main.Writer(ctx, opts...)
}
