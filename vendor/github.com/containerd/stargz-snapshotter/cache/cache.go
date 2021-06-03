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

package cache

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/containerd/stargz-snapshotter/util/lrucache"
	"github.com/containerd/stargz-snapshotter/util/namedmutex"
	"github.com/pkg/errors"
)

const (
	defaultMaxLRUCacheEntry = 10
	defaultMaxCacheFds      = 10
)

type DirectoryCacheConfig struct {

	// Number of entries of LRU cache (default: 10).
	// This won't be used when DataCache is specified.
	MaxLRUCacheEntry int

	// Number of file descriptors to cache (default: 10).
	// This won't be used when FdCache is specified.
	MaxCacheFds int

	// On Add, wait until the data is fully written to the cache directory.
	SyncAdd bool

	// DataCache is an on-memory cache of the data.
	// OnEvicted will be overridden and replaced for internal use.
	DataCache *lrucache.Cache

	// FdCache is a cache for opened file descriptors.
	// OnEvicted will be overridden and replaced for internal use.
	FdCache *lrucache.Cache

	// BufPool will be used for pooling bytes.Buffer.
	BufPool *sync.Pool
}

// TODO: contents validation.

type BlobCache interface {
	// Add adds the passed data to the cache
	Add(key string, p []byte, opts ...Option)

	// FetchAt fetches the specified range of data from the cache
	FetchAt(key string, offset int64, p []byte, opts ...Option) (n int, err error)
}

type cacheOpt struct {
	direct bool
}

type Option func(o *cacheOpt) *cacheOpt

// When Direct option is specified for FetchAt and Add methods, these operation
// won't use on-memory caches. When you know that the targeting value won't be
// used immediately, you can prevent the limited space of on-memory caches from
// being polluted by these unimportant values.
func Direct() Option {
	return func(o *cacheOpt) *cacheOpt {
		o.direct = true
		return o
	}
}

func NewDirectoryCache(directory string, config DirectoryCacheConfig) (BlobCache, error) {
	if !filepath.IsAbs(directory) {
		return nil, fmt.Errorf("dir cache path must be an absolute path; got %q", directory)
	}
	bufPool := config.BufPool
	if bufPool == nil {
		bufPool = &sync.Pool{
			New: func() interface{} {
				return new(bytes.Buffer)
			},
		}
	}
	dataCache := config.DataCache
	if dataCache == nil {
		maxEntry := config.MaxLRUCacheEntry
		if maxEntry == 0 {
			maxEntry = defaultMaxLRUCacheEntry
		}
		dataCache = lrucache.New(maxEntry)
		dataCache.OnEvicted = func(key string, value interface{}) {
			bufPool.Put(value)
		}
	}
	fdCache := config.FdCache
	if fdCache == nil {
		maxEntry := config.MaxCacheFds
		if maxEntry == 0 {
			maxEntry = defaultMaxCacheFds
		}
		fdCache = lrucache.New(maxEntry)
		fdCache.OnEvicted = func(key string, value interface{}) {
			value.(*os.File).Close()
		}
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	dc := &directoryCache{
		cache:     dataCache,
		fileCache: fdCache,
		wipLock:   new(namedmutex.NamedMutex),
		directory: directory,
		bufPool:   bufPool,
	}
	dc.syncAdd = config.SyncAdd
	return dc, nil
}

// directoryCache is a cache implementation which backend is a directory.
type directoryCache struct {
	cache     *lrucache.Cache
	fileCache *lrucache.Cache
	directory string
	wipLock   *namedmutex.NamedMutex

	bufPool *sync.Pool

	syncAdd bool
}

func (dc *directoryCache) FetchAt(key string, offset int64, p []byte, opts ...Option) (n int, err error) {
	opt := &cacheOpt{}
	for _, o := range opts {
		opt = o(opt)
	}

	if !opt.direct {
		// Get data from memory
		if b, done, ok := dc.cache.Get(key); ok {
			defer done()
			data := b.(*bytes.Buffer).Bytes()
			if int64(len(data)) < offset {
				return 0, fmt.Errorf("invalid offset %d exceeds chunk size %d",
					offset, len(data))
			}
			return copy(p, data[offset:]), nil
		}

		// Get data from disk. If the file is already opened, use it.
		if f, done, ok := dc.fileCache.Get(key); ok {
			defer done()
			return f.(*os.File).ReadAt(p, offset)
		}
	}

	// Open the cache file and read the target region
	// TODO: If the target cache is write-in-progress, should we wait for the completion
	//       or simply report the cache miss?
	file, err := os.Open(dc.cachePath(key))
	if err != nil {
		return 0, errors.Wrapf(err, "failed to open blob file for %q", key)
	}
	if n, err = file.ReadAt(p, offset); err == io.EOF {
		err = nil
	}

	// Cache the opened file for future use. If "direct" option is specified, this
	// won't be done. This option is useful for preventing file cache from being
	// polluted by data that won't be accessed immediately.
	if opt.direct {
		file.Close()
	} else {
		_, done, added := dc.fileCache.Add(key, file)
		if !added {
			file.Close() // file already exists in the cache. discard it.
		}
		done() // Release this immediately. This will be removed on eviction in lru cache.
	}

	// TODO: should we cache the entire file data on memory?
	//       but making I/O (possibly huge) on every fetching
	//       might be costly.

	return n, err
}

func (dc *directoryCache) Add(key string, p []byte, opts ...Option) {
	opt := &cacheOpt{}
	for _, o := range opts {
		opt = o(opt)
	}

	if !opt.direct {
		// Cache the passed data on memory. This enables to serve this data even
		// during writing it to the disk. If "direct" option is specified, this
		// won't be done. This option is useful for preventing memory cache from being
		// polluted by data that won't be accessed immediately.
		b := dc.bufPool.Get().(*bytes.Buffer)
		b.Reset()
		b.Write(p)
		_, done, added := dc.cache.Add(key, b)
		if !added {
			dc.bufPool.Put(b) // already exists in the cache. discard it.
		}
		done() // This will remain until it's evicted in lru cache.
	}

	// Cache the passed data to disk.
	b2 := dc.bufPool.Get().(*bytes.Buffer)
	b2.Reset()
	b2.Write(p)
	addFunc := func() {
		defer dc.bufPool.Put(b2)

		var (
			c   = dc.cachePath(key)
			wip = dc.wipPath(key)
		)

		dc.wipLock.Lock(key)
		if _, err := os.Stat(wip); err == nil {
			dc.wipLock.Unlock(key)
			return // Write in progress
		}
		if _, err := os.Stat(c); err == nil {
			dc.wipLock.Unlock(key)
			return // Already exists.
		}

		// Write the contents to a temporary file
		if err := os.MkdirAll(filepath.Dir(wip), os.ModePerm); err != nil {
			fmt.Printf("Warning: Failed to Create blob cache directory %q: %v\n", c, err)
			dc.wipLock.Unlock(key)
			return
		}
		wipfile, err := os.Create(wip)
		if err != nil {
			fmt.Printf("Warning: failed to prepare temp file for storing cache %q", key)
			dc.wipLock.Unlock(key)
			return
		}
		dc.wipLock.Unlock(key)

		defer func() {
			wipfile.Close()
			os.Remove(wipfile.Name())
		}()
		want := b2.Len()
		if _, err := io.CopyN(wipfile, b2, int64(want)); err != nil {
			fmt.Printf("Warning: failed to write cache: %v\n", err)
			return
		}

		// Commit the cache contents
		if err := os.MkdirAll(filepath.Dir(c), os.ModePerm); err != nil {
			fmt.Printf("Warning: Failed to Create blob cache directory %q: %v\n", c, err)
			return
		}
		if err := os.Rename(wipfile.Name(), c); err != nil {
			fmt.Printf("Warning: failed to commit cache to %q: %v\n", c, err)
			return
		}
		file, err := os.Open(c)
		if err != nil {
			fmt.Printf("Warning: failed to open cache on %q: %v\n", c, err)
			return
		}

		// Cache the opened file for future use. If "direct" option is specified, this
		// won't be done. This option is useful for preventing file cache from being
		// polluted by data that won't be accessed immediately.
		if opt.direct {
			file.Close()
		} else {
			_, done, added := dc.fileCache.Add(key, file)
			if !added {
				file.Close() // already exists in the cache. discard it.
			}
			done() // This will remain until it's evicted in lru cache.
		}
	}

	if dc.syncAdd {
		addFunc()
	} else {
		go addFunc()
	}
}

func (dc *directoryCache) cachePath(key string) string {
	return filepath.Join(dc.directory, key[:2], key)
}

func (dc *directoryCache) wipPath(key string) string {
	return filepath.Join(dc.directory, key[:2], "w", key)
}

func NewMemoryCache() BlobCache {
	return &memoryCache{
		membuf: map[string]string{},
	}
}

// memoryCache is a cache implementation which backend is a memory.
type memoryCache struct {
	membuf map[string]string // read-only []byte map is more ideal but we don't have it in golang...
	mu     sync.Mutex
}

func (mc *memoryCache) FetchAt(key string, offset int64, p []byte, opts ...Option) (n int, err error) {
	mc.mu.Lock()
	defer mc.mu.Unlock()

	cache, ok := mc.membuf[key]
	if !ok {
		return 0, fmt.Errorf("Missed cache: %q", key)
	}
	return copy(p, cache[offset:]), nil
}

func (mc *memoryCache) Add(key string, p []byte, opts ...Option) {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	mc.membuf[key] = string(p)
}
