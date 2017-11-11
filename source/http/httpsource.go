package http

import (
	"crypto/sha256"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/locker"
	"github.com/boltdb/bolt"
	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/cache/metadata"
	"github.com/moby/buildkit/snapshot"
	"github.com/moby/buildkit/source"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
	"golang.org/x/net/context"
)

type Opt struct {
	CacheAccessor cache.Accessor
	MetadataStore *metadata.Store
}

type httpSource struct {
	md     *metadata.Store
	cache  cache.Accessor
	locker *locker.Locker
}

func NewSource(opt Opt) (source.Source, error) {
	hs := &httpSource{
		md:     opt.MetadataStore,
		cache:  opt.CacheAccessor,
		locker: locker.NewLocker(),
	}
	return hs, nil
}

func (hs *httpSource) ID() string {
	return source.HttpsScheme
}

type httpSourceHandler struct {
	*httpSource
	src      source.HttpIdentifier
	refID    string
	cacheKey digest.Digest
}

func (hs *httpSource) Resolve(ctx context.Context, id source.Identifier) (source.SourceInstance, error) {
	httpIdentifier, ok := id.(*source.HttpIdentifier)
	if !ok {
		return nil, errors.Errorf("invalid git identifier %v", id)
	}

	return &httpSourceHandler{
		src:        *httpIdentifier,
		httpSource: hs,
	}, nil
}

// urlHash is internal hash the etag is stored by that doesn't leak outside
// this package.
func (hs *httpSourceHandler) urlHash() string {
	return fmt.Sprintf("http-url:%s", digest.FromBytes([]byte(hs.src.URL)))
}

func (hs *httpSourceHandler) CacheKey(ctx context.Context) (string, error) {
	if hs.src.Checksum != "" {
		hs.cacheKey = hs.src.Checksum
		return hs.src.Checksum.String(), nil
	}

	// look up metadata(previously stored headers) for that URL
	sis, err := hs.md.Search(hs.urlHash())
	if err != nil {
		return "", errors.Wrapf(err, "failed to search metadata for %s", hs.urlHash())
	}

	req, err := http.NewRequest("GET", hs.src.URL, nil)
	if err != nil {
		return "", err
	}
	m := map[string]*metadata.StorageItem{}

	if len(sis) > 0 {
		for _, si := range sis {
			if etag := getETag(si); etag != "" {
				if dgst := getChecksum(si); dgst != "" {
					m[etag] = si
					req.Header.Add("If-None-Match", etag)
				}
			}
		}
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}

	if resp.StatusCode == http.StatusNotModified {
		respETag := resp.Header.Get("ETag")
		si, ok := m[respETag]
		if !ok {
			return "", errors.Errorf("invalid not-modified ETag: %v", respETag)
		}
		hs.refID = si.ID()
		dgst := getChecksum(si)
		if dgst == "" {
			return "", errors.Errorf("invalid metadata change")
		}
		resp.Body.Close()
		return dgst.String(), nil
	}

	ref, dgst, err := hs.save(ctx, resp)
	if err != nil {
		return "", err
	}
	ref.Release(context.TODO())

	hs.cacheKey = dgst

	return dgst.String(), nil
}

func (hs *httpSourceHandler) save(ctx context.Context, resp *http.Response) (ref cache.ImmutableRef, dgst digest.Digest, retErr error) {
	newRef, err := hs.cache.New(ctx, nil, cache.CachePolicyRetain, cache.WithDescription(fmt.Sprintf("http url %s", hs.src.URL)))
	if err != nil {
		return nil, "", err
	}

	releaseRef := func() {
		newRef.Release(context.TODO())
	}

	defer func() {
		if retErr != nil && newRef != nil {
			releaseRef()
		}
	}()

	mount, err := newRef.Mount(ctx, false)
	if err != nil {
		return nil, "", err
	}

	lm := snapshot.LocalMounter(mount)
	dir, err := lm.Mount()
	if err != nil {
		return nil, "", err
	}

	defer func() {
		if retErr != nil && lm != nil {
			lm.Unmount()
		}
	}()

	f, err := os.Create(filepath.Join(dir, getFileName(hs.src.URL, resp)))
	if err != nil {
		return nil, "", err
	}
	defer f.Close()

	h := sha256.New()

	if _, err := io.Copy(io.MultiWriter(f, h), resp.Body); err != nil {
		return nil, "", err
	}

	lm.Unmount()
	lm = nil

	ref, err = newRef.Commit(ctx)
	if err != nil {
		return nil, "", err
	}
	newRef = nil

	hs.refID = ref.ID()
	dgst = digest.NewDigest(digest.SHA256, h)

	if respETag := resp.Header.Get("ETag"); respETag != "" {
		setETag(ref.Metadata(), respETag)
		setChecksum(ref.Metadata(), hs.urlHash(), dgst)
		if err := ref.Metadata().Commit(); err != nil {
			return nil, "", err
		}
	}

	return ref, dgst, nil
}

func (hs *httpSourceHandler) Snapshot(ctx context.Context) (cache.ImmutableRef, error) {
	if hs.refID != "" {
		ref, err := hs.cache.Get(ctx, hs.refID)
		if err == nil {
			return ref, nil
		}
	}

	req, err := http.NewRequest("GET", hs.src.URL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}

	ref, dgst, err := hs.save(ctx, resp)
	if err != nil {
		return nil, err
	}
	if dgst != hs.cacheKey {
		ref.Release(context.TODO())
		return nil, errors.Errorf("digest mismatch %s: %s", dgst, hs.cacheKey)
	}

	return ref, errors.Errorf("not-implemented")
}

const keyETag = "etag"
const keyChecksum = "http.checksum"

func setETag(si *metadata.StorageItem, s string) error {
	v, err := metadata.NewValue(s)
	if err != nil {
		return errors.Wrap(err, "failed to create etag value")
	}
	si.Queue(func(b *bolt.Bucket) error {
		return si.SetValue(b, keyETag, v)
	})
	return nil
}

func getETag(si *metadata.StorageItem) string {
	v := si.Get(keyETag)
	if v == nil {
		return ""
	}
	var etag string
	if err := v.Unmarshal(&etag); err != nil {
		return ""
	}
	return etag
}

func setChecksum(si *metadata.StorageItem, url string, d digest.Digest) error {
	v, err := metadata.NewValue(d)
	if err != nil {
		return errors.Wrap(err, "failed to create checksum value")
	}
	v.Index = url
	si.Queue(func(b *bolt.Bucket) error {
		return si.SetValue(b, keyChecksum, v)
	})
	return nil
}

func getChecksum(si *metadata.StorageItem) digest.Digest {
	v := si.Get(keyChecksum)
	if v == nil {
		return ""
	}
	var dgstStr string
	if err := v.Unmarshal(&dgstStr); err != nil {
		return ""
	}
	dgst, err := digest.Parse(dgstStr)
	if err != nil {
		return ""
	}
	return dgst
}

func getFileName(urlStr string, resp *http.Response) string {
	if contentDisposition := resp.Header.Get("Content-Disposition"); contentDisposition != "" {
		if _, params, err := mime.ParseMediaType(contentDisposition); err == nil {
			if params["filename"] != "" && !strings.HasSuffix(params["filename"], "/") {
				if filename := filepath.Base(filepath.FromSlash(params["filename"])); filename != "" {
					return filename
				}
			}
		}
	}
	u, err := url.Parse(urlStr)
	if err == nil {
		if base := path.Base(u.Path); base != "" {
			return base
		}
	}
	return "download"
}
