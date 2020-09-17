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

	"github.com/golang/groupcache/lru"
	"github.com/pkg/errors"
)

const (
	defaultMaxLRUCacheEntry = 10
	defaultMaxCacheFds      = 10
)

type DirectoryCacheConfig struct {
	MaxLRUCacheEntry int
	MaxCacheFds      int
	SyncAdd          bool
}

// TODO: contents validation.

type BlobCache interface {
	Add(key string, p []byte, opts ...Option)
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
	maxEntry := config.MaxLRUCacheEntry
	if maxEntry == 0 {
		maxEntry = defaultMaxLRUCacheEntry
	}
	maxFds := config.MaxCacheFds
	if maxFds == 0 {
		maxFds = defaultMaxCacheFds
	}
	if err := os.MkdirAll(directory, os.ModePerm); err != nil {
		return nil, err
	}
	dc := &directoryCache{
		cache:     newObjectCache(maxEntry),
		fileCache: newObjectCache(maxFds),
		wipLock:   &namedLock{},
		directory: directory,
		bufPool: sync.Pool{
			New: func() interface{} {
				return new(bytes.Buffer)
			},
		},
	}
	dc.cache.finalize = func(value interface{}) {
		dc.bufPool.Put(value)
	}
	dc.fileCache.finalize = func(value interface{}) {
		value.(*os.File).Close()
	}
	dc.syncAdd = config.SyncAdd
	return dc, nil
}

// directoryCache is a cache implementation which backend is a directory.
type directoryCache struct {
	cache     *objectCache
	fileCache *objectCache
	directory string
	wipLock   *namedLock

	bufPool sync.Pool

	syncAdd bool
}

func (dc *directoryCache) FetchAt(key string, offset int64, p []byte, opts ...Option) (n int, err error) {
	opt := &cacheOpt{}
	for _, o := range opts {
		opt = o(opt)
	}

	if !opt.direct {
		// Get data from memory
		if b, done, ok := dc.cache.get(key); ok {
			defer done()
			data := b.(*bytes.Buffer).Bytes()
			if int64(len(data)) < offset {
				return 0, fmt.Errorf("invalid offset %d exceeds chunk size %d",
					offset, len(data))
			}
			return copy(p, data[offset:]), nil
		}

		// Get data from disk. If the file is already opened, use it.
		if f, done, ok := dc.fileCache.get(key); ok {
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
	if opt.direct || !dc.fileCache.add(key, file) {
		file.Close()
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
		if !dc.cache.add(key, b) {
			dc.bufPool.Put(b) // Already exists. No need to cache.
		}
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

		dc.wipLock.lock(key)
		if _, err := os.Stat(wip); err == nil {
			dc.wipLock.unlock(key)
			return // Write in progress
		}
		if _, err := os.Stat(c); err == nil {
			dc.wipLock.unlock(key)
			return // Already exists.
		}

		// Write the contents to a temporary file
		if err := os.MkdirAll(filepath.Dir(wip), os.ModePerm); err != nil {
			fmt.Printf("Warning: Failed to Create blob cache directory %q: %v\n", c, err)
			dc.wipLock.unlock(key)
			return
		}
		wipfile, err := os.Create(wip)
		if err != nil {
			fmt.Printf("Warning: failed to prepare temp file for storing cache %q", key)
			dc.wipLock.unlock(key)
			return
		}
		dc.wipLock.unlock(key)

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
		if opt.direct || !dc.fileCache.add(key, file) {
			file.Close()
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

type namedLock struct {
	muMap  map[string]*sync.Mutex
	refMap map[string]int

	mu sync.Mutex
}

func (nl *namedLock) lock(name string) {
	nl.mu.Lock()
	if nl.muMap == nil {
		nl.muMap = make(map[string]*sync.Mutex)
	}
	if nl.refMap == nil {
		nl.refMap = make(map[string]int)
	}
	if _, ok := nl.muMap[name]; !ok {
		nl.muMap[name] = &sync.Mutex{}
	}
	mu := nl.muMap[name]
	nl.refMap[name]++
	nl.mu.Unlock()
	mu.Lock()
}

func (nl *namedLock) unlock(name string) {
	nl.mu.Lock()
	mu := nl.muMap[name]
	nl.refMap[name]--
	if nl.refMap[name] <= 0 {
		delete(nl.muMap, name)
		delete(nl.refMap, name)
	}
	nl.mu.Unlock()
	mu.Unlock()
}

func newObjectCache(maxEntries int) *objectCache {
	oc := &objectCache{
		cache: lru.New(maxEntries),
	}
	oc.cache.OnEvicted = func(key lru.Key, value interface{}) {
		value.(*object).release() // Decrease ref count incremented in add operation.
	}
	return oc
}

type objectCache struct {
	cache    *lru.Cache
	cacheMu  sync.Mutex
	finalize func(interface{})
}

func (oc *objectCache) get(key string) (value interface{}, done func(), ok bool) {
	oc.cacheMu.Lock()
	defer oc.cacheMu.Unlock()
	o, ok := oc.cache.Get(key)
	if !ok {
		return nil, nil, false
	}
	o.(*object).use()
	return o.(*object).v, func() { o.(*object).release() }, true
}

func (oc *objectCache) add(key string, value interface{}) bool {
	oc.cacheMu.Lock()
	defer oc.cacheMu.Unlock()
	if _, ok := oc.cache.Get(key); ok {
		return false // TODO: should we swap the object?
	}
	o := &object{
		v:        value,
		finalize: oc.finalize,
	}
	o.use() // Keep this object having at least 1 ref count (will be decreased on eviction)
	oc.cache.Add(key, o)
	return true
}

type object struct {
	v interface{}

	refCounts int64
	finalize  func(interface{})

	mu sync.Mutex
}

func (o *object) use() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.refCounts++
}

func (o *object) release() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.refCounts--
	if o.refCounts <= 0 && o.finalize != nil {
		// nobody will refer this object
		o.finalize(o.v)
	}
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
