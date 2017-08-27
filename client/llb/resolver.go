package llb

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/ioutil"
	"net/http"
	"sync"
	"time"

	"github.com/BurntSushi/locker"
	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/errdefs"
	"github.com/containerd/containerd/images"
	"github.com/containerd/containerd/remotes"
	"github.com/containerd/containerd/remotes/docker"
	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
)

var defaultImageMetaResolver ImageMetaResolver
var defaultImageMetaResolverOnce sync.Once

func WithMetaResolver(mr ImageMetaResolver) ImageOption {
	return func(ii *ImageInfo) {
		ii.metaResolver = mr
	}
}

func WithDefaultMetaResolver(ii *ImageInfo) {
	ii.metaResolver = DefaultImageMetaResolver()
}

type ImageMetaResolver interface {
	Resolve(ctx context.Context, ref string) (*ocispec.Image, error)
}

func NewImageMetaResolver() ImageMetaResolver {
	return &imageMetaResolver{
		resolver: docker.NewResolver(docker.ResolverOptions{
			Client: http.DefaultClient,
		}),
		ingester: newInMemoryIngester(),
		cache:    map[string]*ocispec.Image{},
		locker:   locker.NewLocker(),
	}
}

func DefaultImageMetaResolver() ImageMetaResolver {
	defaultImageMetaResolverOnce.Do(func() {
		defaultImageMetaResolver = NewImageMetaResolver()
	})
	return defaultImageMetaResolver
}

type imageMetaResolver struct {
	resolver remotes.Resolver
	ingester *inMemoryIngester
	locker   *locker.Locker
	cache    map[string]*ocispec.Image
}

func (imr *imageMetaResolver) Resolve(ctx context.Context, ref string) (*ocispec.Image, error) {
	imr.locker.Lock(ref)
	defer imr.locker.Unlock(ref)

	if img, ok := imr.cache[ref]; ok {
		return img, nil
	}

	ref, desc, err := imr.resolver.Resolve(ctx, ref)
	if err != nil {
		return nil, err
	}

	fetcher, err := imr.resolver.Fetcher(ctx, ref)
	if err != nil {
		return nil, err
	}

	handlers := []images.Handler{
		remotes.FetchHandler(imr.ingester, fetcher),
		childrenConfigHandler(imr.ingester),
	}
	if err := images.Dispatch(ctx, images.Handlers(handlers...), desc); err != nil {
		return nil, err
	}
	config, err := images.Config(ctx, imr.ingester, desc)
	if err != nil {
		return nil, err
	}

	var ociimage ocispec.Image

	r, err := imr.ingester.Reader(ctx, config.Digest)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	dec := json.NewDecoder(r)
	if err := dec.Decode(&ociimage); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, errors.New("invalid image config")
	}

	imr.cache[ref] = &ociimage

	return &ociimage, nil
}

func childrenConfigHandler(provider content.Provider) images.HandlerFunc {
	return func(ctx context.Context, desc ocispec.Descriptor) ([]ocispec.Descriptor, error) {
		var descs []ocispec.Descriptor
		switch desc.MediaType {
		case images.MediaTypeDockerSchema2Manifest, ocispec.MediaTypeImageManifest:
			p, err := content.ReadBlob(ctx, provider, desc.Digest)
			if err != nil {
				return nil, err
			}

			// TODO(stevvooe): We just assume oci manifest, for now. There may be
			// subtle differences from the docker version.
			var manifest ocispec.Manifest
			if err := json.Unmarshal(p, &manifest); err != nil {
				return nil, err
			}

			descs = append(descs, manifest.Config)
		case images.MediaTypeDockerSchema2ManifestList, ocispec.MediaTypeImageIndex:
			p, err := content.ReadBlob(ctx, provider, desc.Digest)
			if err != nil {
				return nil, err
			}

			var index ocispec.Index
			if err := json.Unmarshal(p, &index); err != nil {
				return nil, err
			}

			descs = append(descs, index.Manifests...)
		case images.MediaTypeDockerSchema2Config, ocispec.MediaTypeImageConfig:
			// childless data types.
			return nil, nil
		default:
			return nil, errors.Errorf("encountered unknown type %v; children may not be fetched", desc.MediaType)
		}

		return descs, nil
	}
}

type inMemoryIngester struct {
	mu      sync.Mutex
	buffers map[digest.Digest][]byte
	refs    map[string]struct{}
}

func newInMemoryIngester() *inMemoryIngester {
	return &inMemoryIngester{
		buffers: map[digest.Digest][]byte{},
		refs:    map[string]struct{}{},
	}
}

func (i *inMemoryIngester) Writer(ctx context.Context, ref string, size int64, expected digest.Digest) (content.Writer, error) {
	i.mu.Lock()
	if _, ok := i.refs[ref]; ok {
		return nil, errors.Wrapf(errdefs.ErrUnavailable, "ref %s locked", ref)
	}
	i.mu.Unlock()
	w := &bufferedWriter{
		ingester: i,
		digester: digest.Canonical.Digester(),
		buffer:   bytes.NewBuffer(nil),
		unlock: func() {
			delete(i.refs, ref)
		},
	}
	return w, nil
}

func (i *inMemoryIngester) Reader(ctx context.Context, dgst digest.Digest) (io.ReadCloser, error) {
	rdr, err := i.reader(ctx, dgst)
	if err != nil {
		return nil, err
	}
	return ioutil.NopCloser(rdr), nil
}

func (i *inMemoryIngester) ReaderAt(ctx context.Context, dgst digest.Digest) (io.ReaderAt, error) {
	return i.reader(ctx, dgst)
}

func (i *inMemoryIngester) reader(ctx context.Context, dgst digest.Digest) (*bytes.Reader, error) {
	i.mu.Lock()
	defer i.mu.Unlock()

	if dt, ok := i.buffers[dgst]; ok {
		return bytes.NewReader(dt), nil
	}

	return nil, errors.Wrapf(errdefs.ErrNotFound, "content %v", dgst)
}

func (i *inMemoryIngester) addMap(k digest.Digest, dt []byte) {
	i.mu.Lock()
	i.mu.Unlock()
	i.buffers[k] = dt
}

type bufferedWriter struct {
	ingester  *inMemoryIngester
	ref       string
	offset    int64
	total     int64
	startedAt time.Time
	updatedAt time.Time
	buffer    *bytes.Buffer
	digester  digest.Digester
	unlock    func()
}

func (w *bufferedWriter) Write(p []byte) (n int, err error) {
	n, err = w.buffer.Write(p)
	w.digester.Hash().Write(p[:n])
	w.offset += int64(len(p))
	w.updatedAt = time.Now()
	return n, err
}

func (w *bufferedWriter) Close() error {
	if w.buffer != nil {
		w.unlock()
		w.buffer = nil
	}
	return nil
}

func (w *bufferedWriter) Status() (content.Status, error) {
	return content.Status{
		Ref:       w.ref,
		Offset:    w.offset,
		Total:     w.total,
		StartedAt: w.startedAt,
		UpdatedAt: w.updatedAt,
	}, nil
}

func (w *bufferedWriter) Digest() digest.Digest {
	return w.digester.Digest()
}

func (w *bufferedWriter) Commit(size int64, expected digest.Digest) error {
	if w.buffer == nil {
		return errors.Errorf("can't commit already committed or closed")
	}
	if size != int64(w.buffer.Len()) {
		return errors.Errorf("%q failed size validation: %v != %v", w.ref, size, int64(w.buffer.Len()))
	}
	dgst := w.digester.Digest()
	if expected != "" && expected != dgst {
		return errors.Errorf("unexpected digest: %v != %v", dgst, expected)
	}
	w.ingester.addMap(dgst, w.buffer.Bytes())
	return w.Close()
}

func (w *bufferedWriter) Truncate(size int64) error {
	if size != 0 {
		return errors.New("Truncate: unsupported size")
	}
	w.offset = 0
	w.digester.Hash().Reset()
	w.buffer.Reset()
	return nil
}
