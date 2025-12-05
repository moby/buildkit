package gateway

import (
	"context"
	"sync"

	api "github.com/containerd/containerd/api/services/content/v1"
	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/plugins/services/content/contentserver"
	cerrdefs "github.com/containerd/errdefs"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"google.golang.org/grpc"
)

type proxyStore struct {
	store content.Store
}

func (s *proxyStore) Register(server *grpc.Server) {
	service := contentserver.New(s.store)
	api.RegisterContentServer(server, service)
}

type filteredStore struct {
	content.Store

	mu      sync.Mutex
	allowed map[digest.Digest]struct{}
}

func newFilteredStore(store content.Store) *filteredStore {
	return &filteredStore{
		Store:   store,
		allowed: make(map[digest.Digest]struct{}),
	}
}

func (cs *filteredStore) allow(descs ...ocispecs.Descriptor) {
	cs.mu.Lock()
	for _, desc := range descs {
		cs.allowed[desc.Digest] = struct{}{}
	}
	cs.mu.Unlock()
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
	allowed := false
	cs.mu.Lock()
	_, allowed = cs.allowed[desc.Digest]
	cs.mu.Unlock()

	if !allowed {
		return nil, cerrdefs.ErrNotFound
	}
	return cs.Store.ReaderAt(ctx, desc)
}

func (cs *filteredStore) Info(ctx context.Context, dgst digest.Digest) (content.Info, error) {
	allowed := false
	cs.mu.Lock()
	_, allowed = cs.allowed[dgst]
	cs.mu.Unlock()

	if !allowed {
		return content.Info{}, cerrdefs.ErrNotFound
	}
	return cs.Store.Info(ctx, dgst)
}
