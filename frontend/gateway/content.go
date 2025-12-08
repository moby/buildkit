package gateway

import (
	"context"
	"sync"

	"github.com/containerd/containerd/v2/core/content"
	cerrdefs "github.com/containerd/errdefs"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
)

type storeFilter struct {
	allowed map[digest.Digest]ocispecs.Descriptor
	mu      sync.Mutex
}

func (f *storeFilter) allow(descs ...ocispecs.Descriptor) {
	f.mu.Lock()
	if f.allowed == nil {
		f.allowed = make(map[digest.Digest]ocispecs.Descriptor)
	}
	for _, desc := range descs {
		f.allowed[desc.Digest] = desc
	}
	f.mu.Unlock()
}

func (f *storeFilter) isAllowed(desc ocispecs.Descriptor) bool {
	f.mu.Lock()
	_, allowed := f.allowed[desc.Digest]
	f.mu.Unlock()
	return allowed
}

func (f *storeFilter) isDigestAllowed(dgst digest.Digest) bool {
	f.mu.Lock()
	_, allowed := f.allowed[dgst]
	f.mu.Unlock()
	return allowed
}

type filteredStore struct {
	content.Store
	*storeFilter
}

func newFilteredStore(store content.Store, filter *storeFilter) content.Store {
	return &filteredStore{
		Store:       store,
		storeFilter: filter,
	}
}

func (cs *filteredStore) Writer(ctx context.Context, opts ...content.WriterOpt) (content.Writer, error) {
	return nil, errors.Errorf("read-only content store")
}

func (cs *filteredStore) Delete(ctx context.Context, dgst digest.Digest) error {
	return errors.Errorf("read-only content store")
}

func (cs *filteredStore) Update(ctx context.Context, info content.Info, fieldpaths ...string) (content.Info, error) {
	return content.Info{}, errors.Errorf("read-only content store")
}

func (cs *filteredStore) Abort(ctx context.Context, ref string) error {
	return errors.Errorf("read-only content store")
}

func (cs *filteredStore) ReaderAt(ctx context.Context, desc ocispecs.Descriptor) (content.ReaderAt, error) {
	if !cs.isAllowed(desc) {
		return nil, cerrdefs.ErrNotFound
	}
	return cs.Store.ReaderAt(ctx, desc)
}

func (cs *filteredStore) Info(ctx context.Context, dgst digest.Digest) (content.Info, error) {
	if !cs.isDigestAllowed(dgst) {
		return content.Info{}, cerrdefs.ErrNotFound
	}
	return cs.Store.Info(ctx, dgst)
}
