package containerd

import (
	"context"

	"github.com/containerd/containerd/images"
	"github.com/containerd/containerd/namespaces"
)

type nsImageStore struct {
	ns string
	images.Store
}

func (i *nsImageStore) Get(ctx context.Context, name string) (images.Image, error) {
	ctx = namespaces.WithNamespace(ctx, i.ns)
	return i.Store.Get(ctx, name)
}

func (i *nsImageStore) List(ctx context.Context, filters ...string) ([]images.Image, error) {
	ctx = namespaces.WithNamespace(ctx, i.ns)
	return i.Store.List(ctx, filters...)
}

func (i *nsImageStore) Create(ctx context.Context, image images.Image) (images.Image, error) {
	ctx = namespaces.WithNamespace(ctx, i.ns)
	return i.Store.Create(ctx, image)
}

func (i *nsImageStore) Update(ctx context.Context, image images.Image, fieldpaths ...string) (images.Image, error) {
	ctx = namespaces.WithNamespace(ctx, i.ns)
	return i.Store.Update(ctx, image, fieldpaths...)
}

func (i *nsImageStore) Delete(ctx context.Context, name string, opts ...images.DeleteOpt) error {
	ctx = namespaces.WithNamespace(ctx, i.ns)
	return i.Store.Delete(ctx, name, opts...)
}
