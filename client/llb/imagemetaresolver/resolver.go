package imagemetaresolver

import (
	"bytes"
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/BurntSushi/locker"
	"github.com/containerd/containerd/content"
	"github.com/containerd/containerd/errdefs"
	"github.com/containerd/containerd/remotes"
	"github.com/containerd/containerd/remotes/docker"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/util/imageutil"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
	netcontext "golang.org/x/net/context"
)

var defaultImageMetaResolver llb.ImageMetaResolver
var defaultImageMetaResolverOnce sync.Once

var WithDefault = llb.ImageOptionFunc(func(ii *llb.ImageInfo) {
	llb.WithMetaResolver(Default()).SetImageOption(ii)
})

func New() llb.ImageMetaResolver {
	return &imageMetaResolver{
		resolver: docker.NewResolver(docker.ResolverOptions{
			Client: http.DefaultClient,
		}),
		ingester: newInMemoryIngester(),
		cache:    map[string]resolveResult{},
		locker:   locker.NewLocker(),
	}
}

func Default() llb.ImageMetaResolver {
	defaultImageMetaResolverOnce.Do(func() {
		defaultImageMetaResolver = New()
	})
	return defaultImageMetaResolver
}

type imageMetaResolver struct {
	resolver remotes.Resolver
	ingester *inMemoryIngester
	locker   *locker.Locker
	cache    map[string]resolveResult
}

type resolveResult struct {
	config []byte
	dgst   digest.Digest
}

func (imr *imageMetaResolver) ResolveImageConfig(ctx netcontext.Context, ref string) (digest.Digest, []byte, error) {
	imr.locker.Lock(ref)
	defer imr.locker.Unlock(ref)

	if res, ok := imr.cache[ref]; ok {
		return res.dgst, res.config, nil
	}

	dgst, config, err := imageutil.Config(ctx, ref, imr.resolver, imr.ingester)
	if err != nil {
		return "", nil, err
	}

	imr.cache[ref] = resolveResult{dgst: dgst, config: config}
	return dgst, config, nil
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

func (i *inMemoryIngester) ReaderAt(ctx context.Context, dgst digest.Digest) (content.ReaderAt, error) {
	r, err := i.reader(ctx, dgst)
	if err != nil {
		return nil, err
	}
	return &contentReaderAt{r}, nil
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

type contentReaderAt struct {
	*bytes.Reader
}

func (c *contentReaderAt) Close() error {
	return nil
}

func (c *contentReaderAt) Size() int64 {
	return int64(c.Reader.Len())
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

func (w *bufferedWriter) Commit(ctx context.Context, size int64, expected digest.Digest, opt ...content.Opt) error {
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
