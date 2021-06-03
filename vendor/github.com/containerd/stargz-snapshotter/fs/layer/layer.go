/*
   Copyright The containerd Authors.

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

/*
   Copyright 2019 The Go Authors. All rights reserved.
   Use of this source code is governed by a BSD-style
   license that can be found in the NOTICE.md file.
*/

package layer

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/containerd/containerd/log"
	"github.com/containerd/containerd/reference"
	"github.com/containerd/containerd/remotes/docker"
	"github.com/containerd/stargz-snapshotter/cache"
	"github.com/containerd/stargz-snapshotter/estargz"
	"github.com/containerd/stargz-snapshotter/fs/config"
	"github.com/containerd/stargz-snapshotter/fs/reader"
	"github.com/containerd/stargz-snapshotter/fs/remote"
	"github.com/containerd/stargz-snapshotter/task"
	"github.com/containerd/stargz-snapshotter/util/lrucache"
	"github.com/golang/groupcache/lru"
	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"golang.org/x/sync/singleflight"
)

const (
	defaultResolveResultEntry = 30
	defaultMaxLRUCacheEntry   = 10
	defaultMaxCacheFds        = 10
	defaultPrefetchTimeoutSec = 10
	memoryCacheType           = "memory"
)

// Layer represents a layer.
type Layer interface {

	// Info returns the information of this layer.
	Info() Info

	// Root returns the root node of this layer.
	Root() *estargz.TOCEntry

	// Check checks if the layer is still connectable.
	Check() error

	// Refresh refreshes the layer connection.
	Refresh(ctx context.Context, hosts docker.RegistryHosts, refspec reference.Spec, desc ocispec.Descriptor) error

	// Verify verifies this layer using the passed TOC Digest.
	Verify(tocDigest digest.Digest) (err error)

	// SkipVerify skips verification for this layer.
	SkipVerify()

	// OpenFile opens a file.
	// Calling this function before calling Verify or SkipVerify will fail.
	OpenFile(name string) (io.ReaderAt, error)

	// Prefetch prefetches the specified size. If the layer is eStargz and contains landmark files,
	// the range indicated by these files is respected.
	// Calling this function before calling Verify or SkipVerify will fail.
	Prefetch(prefetchSize int64) error

	// WaitForPrefetchCompletion waits untils Prefetch completes.
	WaitForPrefetchCompletion() error

	// BackgroundFetch fetches the entire layer contents to the cache.
	// Fetching contents is done as a background task.
	// Calling this function before calling Verify or SkipVerify will fail.
	BackgroundFetch() error
}

// Info is the current status of a layer.
type Info struct {
	Digest      digest.Digest
	Size        int64
	FetchedSize int64
}

// Resolver resolves the layer location and provieds the handler of that layer.
type Resolver struct {
	resolver              *remote.Resolver
	prefetchTimeout       time.Duration
	layerCache            *lru.Cache
	layerCacheMu          sync.Mutex
	blobCache             *lru.Cache
	blobCacheMu           sync.Mutex
	backgroundTaskManager *task.BackgroundTaskManager
	fsCache               cache.BlobCache
	resolveG              singleflight.Group
}

// NewResolver returns a new layer resolver.
func NewResolver(root string, backgroundTaskManager *task.BackgroundTaskManager, cfg config.Config) (*Resolver, error) {
	resolveResultEntry := cfg.ResolveResultEntry
	if resolveResultEntry == 0 {
		resolveResultEntry = defaultResolveResultEntry
	}
	prefetchTimeout := time.Duration(cfg.PrefetchTimeoutSec) * time.Second
	if prefetchTimeout == 0 {
		prefetchTimeout = defaultPrefetchTimeoutSec * time.Second
	}

	// Prepare contents cache
	fsCache, err := newCache(filepath.Join(root, "fscache"), cfg.FSCacheType, cfg)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to create fs cache")
	}
	httpCache, err := newCache(filepath.Join(root, "httpcache"), cfg.HTTPCacheType, cfg)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to create http cache")
	}

	return &Resolver{
		resolver:              remote.NewResolver(httpCache, cfg.BlobConfig),
		fsCache:               fsCache,
		layerCache:            lru.New(resolveResultEntry),
		blobCache:             lru.New(resolveResultEntry),
		prefetchTimeout:       prefetchTimeout,
		backgroundTaskManager: backgroundTaskManager,
	}, nil
}

func newCache(cachepath string, cacheType string, cfg config.Config) (cache.BlobCache, error) {
	if cacheType == memoryCacheType {
		return cache.NewMemoryCache(), nil
	}

	dcc := cfg.DirectoryCacheConfig
	maxDataEntry := dcc.MaxLRUCacheEntry
	if maxDataEntry == 0 {
		maxDataEntry = defaultMaxLRUCacheEntry
	}
	maxFdEntry := dcc.MaxCacheFds
	if maxFdEntry == 0 {
		maxFdEntry = defaultMaxCacheFds
	}

	bufPool := &sync.Pool{
		New: func() interface{} {
			return new(bytes.Buffer)
		},
	}
	dCache, fCache := lrucache.New(maxDataEntry), lrucache.New(maxFdEntry)
	dCache.OnEvicted = func(key string, value interface{}) {
		bufPool.Put(value)
	}
	fCache.OnEvicted = func(key string, value interface{}) {
		value.(*os.File).Close()
	}
	return cache.NewDirectoryCache(
		cachepath,
		cache.DirectoryCacheConfig{
			SyncAdd:   dcc.SyncAdd,
			DataCache: dCache,
			FdCache:   fCache,
			BufPool:   bufPool,
		},
	)
}

// Resolve resolves a layer based on the passed layer blob information.
func (r *Resolver) Resolve(ctx context.Context, hosts docker.RegistryHosts, refspec reference.Spec, desc ocispec.Descriptor) (_ Layer, retErr error) {
	name := refspec.String() + "/" + desc.Digest.String()
	ctx = log.WithLogger(ctx, log.G(ctx).WithField("src", name))

	// First, try to retrieve this layer from the underlying LRU cache.
	r.layerCacheMu.Lock()
	c, ok := r.layerCache.Get(name)
	r.layerCacheMu.Unlock()
	if ok && c.(*layer).Check() == nil {
		return c.(*layer), nil
	}

	resultChan := r.resolveG.DoChan(name, func() (interface{}, error) {
		log.G(ctx).Debugf("resolving")

		// Resolve the blob.
		blobR, err := r.resolveBlob(ctx, hosts, refspec, desc)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to resolve the blob")
		}

		// Get a reader for stargz archive.
		// Each file's read operation is a prioritized task and all background tasks
		// will be stopped during the execution so this can avoid being disturbed for
		// NW traffic by background tasks.
		sr := io.NewSectionReader(readerAtFunc(func(p []byte, offset int64) (n int, err error) {
			r.backgroundTaskManager.DoPrioritizedTask()
			defer r.backgroundTaskManager.DonePrioritizedTask()
			return blobR.ReadAt(p, offset)
		}), 0, blobR.Size())
		vr, root, err := reader.NewReader(sr, r.fsCache)
		if err != nil {
			return nil, errors.Wrap(err, "failed to read layer")
		}

		// Combine layer information together
		l := newLayer(r, desc, blobR, vr, root)
		r.layerCacheMu.Lock()
		r.layerCache.Add(name, l)
		r.layerCacheMu.Unlock()

		log.G(ctx).Debugf("resolved")
		return l, nil
	})

	var res singleflight.Result
	select {
	case res = <-resultChan:
	case <-time.After(30 * time.Second):
		r.resolveG.Forget(name)
		return nil, fmt.Errorf("failed to resolve layer (timeout)")
	}
	if res.Err != nil || res.Val == nil {
		return nil, fmt.Errorf("failed to resolve layer: %v", res.Err)
	}

	return res.Val.(*layer), nil
}

// resolveBlob resolves a blob based on the passed layer blob information.
func (r *Resolver) resolveBlob(ctx context.Context, hosts docker.RegistryHosts, refspec reference.Spec, desc ocispec.Descriptor) (remote.Blob, error) {
	name := refspec.String() + "/" + desc.Digest.String()

	// Resolve the blob. The result will be cached for future use. This is effective
	// in some failure cases including resolving is succeeded but the blob is non-stargz.
	var blob remote.Blob
	r.blobCacheMu.Lock()
	c, ok := r.blobCache.Get(name)
	r.blobCacheMu.Unlock()
	if ok {
		if blob := c.(remote.Blob); blob.Check() == nil {
			return blob, nil
		}
	}

	var err error
	blob, err = r.resolver.Resolve(ctx, hosts, refspec, desc)
	if err != nil {
		log.G(ctx).WithError(err).Debugf("failed to resolve source")
		return nil, errors.Wrap(err, "failed to resolve the source")
	}
	r.blobCacheMu.Lock()
	r.blobCache.Add(name, blob)
	r.blobCacheMu.Unlock()

	return blob, nil
}

func newLayer(
	resolver *Resolver,
	desc ocispec.Descriptor,
	blob remote.Blob,
	vr *reader.VerifiableReader,
	root *estargz.TOCEntry,
) *layer {
	return &layer{
		resolver:         resolver,
		desc:             desc,
		blob:             blob,
		verifiableReader: vr,
		root:             root,
		prefetchWaiter:   newWaiter(),
	}
}

type layer struct {
	resolver         *Resolver
	desc             ocispec.Descriptor
	blob             remote.Blob
	verifiableReader *reader.VerifiableReader
	root             *estargz.TOCEntry
	prefetchWaiter   *waiter

	r reader.Reader
}

func (l *layer) Info() Info {
	return Info{
		Digest:      l.desc.Digest,
		Size:        l.blob.Size(),
		FetchedSize: l.blob.FetchedSize(),
	}
}

func (l *layer) Root() *estargz.TOCEntry {
	return l.root
}

func (l *layer) Check() error {
	return l.blob.Check()
}

func (l *layer) Refresh(ctx context.Context, hosts docker.RegistryHosts, refspec reference.Spec, desc ocispec.Descriptor) error {
	return l.blob.Refresh(ctx, hosts, refspec, desc)
}

func (l *layer) Verify(tocDigest digest.Digest) (err error) {
	l.r, err = l.verifiableReader.VerifyTOC(tocDigest)
	return
}

func (l *layer) SkipVerify() {
	l.r = l.verifiableReader.SkipVerify()
}

func (l *layer) OpenFile(name string) (io.ReaderAt, error) {
	if l.r == nil {
		return nil, fmt.Errorf("layer hasn't been verified yet")
	}
	return l.r.OpenFile(name)
}

func (l *layer) Prefetch(prefetchSize int64) error {
	defer l.prefetchWaiter.done() // Notify the completion

	if l.r == nil {
		return fmt.Errorf("layer hasn't been verified yet")
	}
	lr := l.r
	if _, ok := lr.Lookup(estargz.NoPrefetchLandmark); ok {
		// do not prefetch this layer
		return nil
	} else if e, ok := lr.Lookup(estargz.PrefetchLandmark); ok {
		// override the prefetch size with optimized value
		prefetchSize = e.Offset
	} else if prefetchSize > l.blob.Size() {
		// adjust prefetch size not to exceed the whole layer size
		prefetchSize = l.blob.Size()
	}

	// Fetch the target range
	if err := l.blob.Cache(0, prefetchSize); err != nil {
		return errors.Wrap(err, "failed to prefetch layer")
	}

	// Cache uncompressed contents of the prefetched range
	if err := lr.Cache(reader.WithFilter(func(e *estargz.TOCEntry) bool {
		return e.Offset < prefetchSize // Cache only prefetch target
	})); err != nil {
		return errors.Wrap(err, "failed to cache prefetched layer")
	}

	return nil
}

func (l *layer) WaitForPrefetchCompletion() error {
	return l.prefetchWaiter.wait(l.resolver.prefetchTimeout)
}

func (l *layer) BackgroundFetch() error {
	if l.r == nil {
		return fmt.Errorf("layer hasn't been verified yet")
	}
	lr := l.r
	br := io.NewSectionReader(readerAtFunc(func(p []byte, offset int64) (retN int, retErr error) {
		l.resolver.backgroundTaskManager.InvokeBackgroundTask(func(ctx context.Context) {
			retN, retErr = l.blob.ReadAt(
				p,
				offset,
				remote.WithContext(ctx),              // Make cancellable
				remote.WithCacheOpts(cache.Direct()), // Do not pollute mem cache
			)
		}, 120*time.Second)
		return
	}), 0, l.blob.Size())
	return lr.Cache(
		reader.WithReader(br),                // Read contents in background
		reader.WithCacheOpts(cache.Direct()), // Do not pollute mem cache
	)
}

func newWaiter() *waiter {
	return &waiter{
		completionCond: sync.NewCond(&sync.Mutex{}),
	}
}

type waiter struct {
	isDone         bool
	isDoneMu       sync.Mutex
	completionCond *sync.Cond
}

func (w *waiter) done() {
	w.isDoneMu.Lock()
	w.isDone = true
	w.isDoneMu.Unlock()
	w.completionCond.Broadcast()
}

func (w *waiter) wait(timeout time.Duration) error {
	wait := func() <-chan struct{} {
		ch := make(chan struct{})
		go func() {
			w.isDoneMu.Lock()
			isDone := w.isDone
			w.isDoneMu.Unlock()

			w.completionCond.L.Lock()
			if !isDone {
				w.completionCond.Wait()
			}
			w.completionCond.L.Unlock()
			ch <- struct{}{}
		}()
		return ch
	}
	select {
	case <-time.After(timeout):
		w.isDoneMu.Lock()
		w.isDone = true
		w.isDoneMu.Unlock()
		w.completionCond.Broadcast()
		return fmt.Errorf("timeout(%v)", timeout)
	case <-wait():
		return nil
	}
}

type readerAtFunc func([]byte, int64) (int, error)

func (f readerAtFunc) ReadAt(p []byte, offset int64) (int, error) { return f(p, offset) }
