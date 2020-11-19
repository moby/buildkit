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

package remote

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"regexp"
	"sync"
	"time"

	"github.com/containerd/containerd/reference"
	"github.com/containerd/containerd/remotes/docker"
	"github.com/containerd/stargz-snapshotter/cache"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
)

var contentRangeRegexp = regexp.MustCompile(`bytes ([0-9]+)-([0-9]+)/([0-9]+|\\*)`)

type Blob interface {
	Check() error
	Size() int64
	FetchedSize() int64
	ReadAt(p []byte, offset int64, opts ...Option) (int, error)
	Cache(offset int64, size int64, opts ...Option) error
	Refresh(ctx context.Context, host docker.RegistryHosts, refspec reference.Spec, desc ocispec.Descriptor) error
}

type blob struct {
	fetcher   *fetcher
	fetcherMu sync.Mutex

	size          int64
	chunkSize     int64
	cache         cache.BlobCache
	lastCheck     time.Time
	lastCheckMu   sync.Mutex
	checkInterval time.Duration

	fetchedRegionSet   regionSet
	fetchedRegionSetMu sync.Mutex

	resolver *Resolver
}

func (b *blob) Refresh(ctx context.Context, hosts docker.RegistryHosts, refspec reference.Spec, desc ocispec.Descriptor) error {
	// refresh the fetcher
	new, newSize, err := newFetcher(ctx, hosts, refspec, desc)
	if err != nil {
		return err
	} else if newSize != b.size {
		return fmt.Errorf("Invalid size of new blob %d; want %d", newSize, b.size)
	}

	// update the blob's fetcher with new one
	b.fetcherMu.Lock()
	b.fetcher = new
	b.fetcherMu.Unlock()
	b.lastCheckMu.Lock()
	b.lastCheck = time.Now()
	b.lastCheckMu.Unlock()

	return nil
}

func (b *blob) Check() error {
	now := time.Now()
	b.lastCheckMu.Lock()
	lastCheck := b.lastCheck
	b.lastCheckMu.Unlock()
	if now.Sub(lastCheck) < b.checkInterval {
		// do nothing if not expired
		return nil
	}
	b.fetcherMu.Lock()
	fr := b.fetcher
	b.fetcherMu.Unlock()
	err := fr.check()
	if err == nil {
		// update lastCheck only if check succeeded.
		// on failure, we should check this layer next time again.
		b.lastCheckMu.Lock()
		b.lastCheck = now
		b.lastCheckMu.Unlock()
	}

	return err
}

func (b *blob) Size() int64 {
	return b.size
}

func (b *blob) FetchedSize() int64 {
	b.fetchedRegionSetMu.Lock()
	sz := b.fetchedRegionSet.totalSize()
	b.fetchedRegionSetMu.Unlock()
	return sz
}

func (b *blob) Cache(offset int64, size int64, opts ...Option) error {
	var cacheOpts options
	for _, o := range opts {
		o(&cacheOpts)
	}

	b.fetcherMu.Lock()
	fr := b.fetcher
	b.fetcherMu.Unlock()

	fetchReg := region{floor(offset, b.chunkSize), ceil(offset+size-1, b.chunkSize) - 1}
	discard := make(map[region]io.Writer)
	b.walkChunks(fetchReg, func(reg region) error {
		if _, err := b.cache.FetchAt(fr.genID(reg), 0, nil, cacheOpts.cacheOpts...); err != nil {
			discard[reg] = ioutil.Discard
		}
		return nil
	})
	if err := b.fetchRange(discard, &cacheOpts); err != nil {
		return err
	}

	return nil
}

// ReadAt reads remote chunks from specified offset for the buffer size.
// It tries to fetch as many chunks as possible from local cache.
// We can configure this function with options.
func (b *blob) ReadAt(p []byte, offset int64, opts ...Option) (int, error) {
	if len(p) == 0 || offset > b.size {
		return 0, nil
	}

	// Make the buffer chunk aligned
	allRegion := region{floor(offset, b.chunkSize), ceil(offset+int64(len(p))-1, b.chunkSize) - 1}
	allData := make(map[region]io.Writer)
	var putBufs []*bytes.Buffer
	defer func() {
		for _, bf := range putBufs {
			b.resolver.bufPool.Put(bf)
		}
	}()

	var readAtOpts options
	for _, o := range opts {
		o(&readAtOpts)
	}

	// Fetcher can be suddenly updated so we take and use the snapshot of it for
	// consistency.
	b.fetcherMu.Lock()
	fr := b.fetcher
	b.fetcherMu.Unlock()

	var commits []func() error
	b.walkChunks(allRegion, func(chunk region) error {
		var (
			base         = positive(chunk.b - offset)
			lowerUnread  = positive(offset - chunk.b)
			upperUnread  = positive(chunk.e + 1 - (offset + int64(len(p))))
			expectedSize = chunk.size() - upperUnread - lowerUnread
		)

		// Check if the content exists in the cache
		n, err := b.cache.FetchAt(fr.genID(chunk), lowerUnread, p[base:base+expectedSize], readAtOpts.cacheOpts...)
		if err == nil && n == int(expectedSize) {
			return nil
		}

		// We missed cache. Take it from remote registry.
		// We get the whole chunk here and add it to the cache so that following
		// reads against neighboring chunks can take the data without making HTTP requests.
		if lowerUnread == 0 && upperUnread == 0 {
			// We can directly store the result in the given buffer
			allData[chunk] = &byteWriter{
				p: p[base : base+chunk.size()],
			}
		} else {
			// Use temporally buffer for aligning this chunk
			bf := b.resolver.bufPool.Get().(*bytes.Buffer)
			putBufs = append(putBufs, bf)
			bf.Reset()
			bf.Grow(int(chunk.size()))
			allData[chunk] = bf

			// Function for committing the buffered chunk into the result slice.
			commits = append(commits, func() error {
				if int64(bf.Len()) != chunk.size() {
					return fmt.Errorf("unexpected data size %d; want %d",
						bf.Len(), chunk.size())
				}
				bb := bf.Bytes()[:chunk.size()]
				n := copy(p[base:], bb[lowerUnread:chunk.size()-upperUnread])
				if int64(n) != expectedSize {
					return fmt.Errorf("invalid copied data size %d; want %d",
						n, expectedSize)
				}
				return nil
			})
		}
		return nil
	})

	// Read required data
	if err := b.fetchRange(allData, &readAtOpts); err != nil {
		return 0, err
	}

	// Write all data to the result buffer
	for _, c := range commits {
		if err := c(); err != nil {
			return 0, err
		}
	}

	// Adjust the buffer size according to the blob size
	if remain := b.size - offset; int64(len(p)) >= remain {
		if remain < 0 {
			remain = 0
		}
		p = p[:remain]
	}

	return len(p), nil
}

// fetchRange fetches all specified chunks from local cache and remote blob.
func (b *blob) fetchRange(allData map[region]io.Writer, opts *options) error {
	if len(allData) == 0 {
		return nil
	}

	// Fetcher can be suddenly updated so we take and use the snapshot of it for
	// consistency.
	b.fetcherMu.Lock()
	fr := b.fetcher
	b.fetcherMu.Unlock()

	// request missed regions
	var req []region
	fetched := make(map[region]bool)
	for reg := range allData {
		req = append(req, reg)
		fetched[reg] = false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	mr, err := fr.fetch(ctx, req, true, opts)
	if err != nil {
		return err
	}
	defer mr.Close()

	// Update the check timer because we succeeded to access the blob
	b.lastCheckMu.Lock()
	b.lastCheck = time.Now()
	b.lastCheckMu.Unlock()

	// chunk and cache responsed data. Regions must be aligned by chunk size.
	// TODO: Reorganize remoteData to make it be aligned by chunk size
	for {
		reg, p, err := mr.Next()
		if err == io.EOF {
			break
		} else if err != nil {
			return errors.Wrapf(err, "failed to read multipart resp")
		}
		if err := b.walkChunks(reg, func(chunk region) error {

			// Prepare the temporary buffer
			bf := b.resolver.bufPool.Get().(*bytes.Buffer)
			defer b.resolver.bufPool.Put(bf)
			bf.Reset()
			bf.Grow(int(chunk.size()))
			w := io.Writer(bf)

			// If this chunk is one of the targets, write the content to the
			// passed reader too.
			if _, ok := fetched[chunk]; ok {
				w = io.MultiWriter(bf, allData[chunk])
			}

			// Copy the target chunk
			if _, err := io.CopyN(w, p, chunk.size()); err != nil {
				return err
			} else if int64(bf.Len()) != chunk.size() {
				return fmt.Errorf("unexpected fetched data size %d; want %d",
					bf.Len(), chunk.size())
			}

			// Add the target chunk to the cache
			b.cache.Add(fr.genID(chunk), bf.Bytes()[:chunk.size()], opts.cacheOpts...)
			b.fetchedRegionSetMu.Lock()
			b.fetchedRegionSet.add(chunk)
			b.fetchedRegionSetMu.Unlock()
			fetched[chunk] = true
			return nil
		}); err != nil {
			return errors.Wrapf(err, "failed to get chunks")
		}
	}

	// Check all chunks are fetched
	var unfetched []region
	for c, b := range fetched {
		if !b {
			unfetched = append(unfetched, c)
		}
	}
	if unfetched != nil {
		return fmt.Errorf("failed to fetch region %v", unfetched)
	}

	return nil
}

type walkFunc func(reg region) error

// walkChunks walks chunks from begin to end in order in the specified region.
// specified region must be aligned by chunk size.
func (b *blob) walkChunks(allRegion region, walkFn walkFunc) error {
	if allRegion.b%b.chunkSize != 0 {
		return fmt.Errorf("region (%d, %d) must be aligned by chunk size",
			allRegion.b, allRegion.e)
	}
	for i := allRegion.b; i <= allRegion.e && i < b.size; i += b.chunkSize {
		reg := region{i, i + b.chunkSize - 1}
		if reg.e >= b.size {
			reg.e = b.size - 1
		}
		if err := walkFn(reg); err != nil {
			return err
		}
	}
	return nil
}

type byteWriter struct {
	p []byte
	n int
}

func (w *byteWriter) Write(p []byte) (int, error) {
	n := copy(w.p[w.n:], p)
	w.n += n
	return n, nil
}

func floor(n int64, unit int64) int64 {
	return (n / unit) * unit
}

func ceil(n int64, unit int64) int64 {
	return (n/unit + 1) * unit
}

func positive(n int64) int64 {
	if n < 0 {
		return 0
	}
	return n
}
