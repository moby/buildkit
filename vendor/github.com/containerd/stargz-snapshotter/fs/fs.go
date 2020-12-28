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

//
// Example implementation of FileSystem.
//
// This implementation uses stargz by CRFS(https://github.com/google/crfs) as
// image format, which has following feature:
// - We can use docker registry as a backend store (means w/o additional layer
//   stores).
// - The stargz-formatted image is still docker-compatible (means normal
//   runtimes can still use the formatted image).
//
// Currently, we reimplemented CRFS-like filesystem for ease of integration.
// But in the near future, we intend to integrate it with CRFS.
//

package fs

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/containerd/containerd/log"
	"github.com/containerd/containerd/reference"
	"github.com/containerd/containerd/remotes/docker"
	"github.com/containerd/stargz-snapshotter/cache"
	"github.com/containerd/stargz-snapshotter/estargz"
	"github.com/containerd/stargz-snapshotter/fs/config"
	"github.com/containerd/stargz-snapshotter/fs/reader"
	"github.com/containerd/stargz-snapshotter/fs/remote"
	"github.com/containerd/stargz-snapshotter/fs/source"
	snbase "github.com/containerd/stargz-snapshotter/snapshot"
	"github.com/containerd/stargz-snapshotter/task"
	"github.com/golang/groupcache/lru"
	fusefs "github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"golang.org/x/sync/singleflight"
	"golang.org/x/sys/unix"
)

const (
	blockSize                 = 4096
	memoryCacheType           = "memory"
	whiteoutPrefix            = ".wh."
	whiteoutOpaqueDir         = whiteoutPrefix + whiteoutPrefix + ".opq"
	opaqueXattr               = "trusted.overlay.opaque"
	opaqueXattrValue          = "y"
	stateDirName              = ".stargz-snapshotter"
	defaultResolveResultEntry = 100
	defaultPrefetchTimeoutSec = 10
	statFileMode              = syscall.S_IFREG | 0400 // -r--------
	stateDirMode              = syscall.S_IFDIR | 0500 // dr-x------
)

type Option func(*options)

type options struct {
	getSources source.GetSources
}

func WithGetSources(s source.GetSources) Option {
	return func(opts *options) {
		opts.getSources = s
	}
}

func NewFilesystem(root string, cfg config.Config, opts ...Option) (_ snbase.FileSystem, err error) {
	var fsOpts options
	for _, o := range opts {
		o(&fsOpts)
	}

	dcc := cfg.DirectoryCacheConfig
	var httpCache cache.BlobCache
	if cfg.HTTPCacheType == memoryCacheType {
		httpCache = cache.NewMemoryCache()
	} else {
		if httpCache, err = cache.NewDirectoryCache(
			filepath.Join(root, "http"),
			cache.DirectoryCacheConfig{
				MaxLRUCacheEntry: dcc.MaxLRUCacheEntry,
				MaxCacheFds:      dcc.MaxCacheFds,
				SyncAdd:          dcc.SyncAdd,
			},
		); err != nil {
			return nil, errors.Wrap(err, "failed to prepare HTTP cache")
		}
	}
	var fsCache cache.BlobCache
	if cfg.FSCacheType == memoryCacheType {
		fsCache = cache.NewMemoryCache()
	} else {
		if fsCache, err = cache.NewDirectoryCache(
			filepath.Join(root, "fscache"),
			cache.DirectoryCacheConfig{
				MaxLRUCacheEntry: dcc.MaxLRUCacheEntry,
				MaxCacheFds:      dcc.MaxCacheFds,
				SyncAdd:          dcc.SyncAdd,
			},
		); err != nil {
			return nil, errors.Wrap(err, "failed to prepare filesystem cache")
		}
	}
	resolveResultEntry := cfg.ResolveResultEntry
	if resolveResultEntry == 0 {
		resolveResultEntry = defaultResolveResultEntry
	}
	prefetchTimeout := time.Duration(cfg.PrefetchTimeoutSec) * time.Second
	if prefetchTimeout == 0 {
		prefetchTimeout = defaultPrefetchTimeoutSec * time.Second
	}
	getSources := fsOpts.getSources
	if getSources == nil {
		getSources = source.FromDefaultLabels(
			docker.ConfigureDefaultRegistries(docker.WithPlainHTTP(docker.MatchLocalhost)))
	}
	return &filesystem{
		resolver:              remote.NewResolver(httpCache, cfg.BlobConfig),
		getSources:            getSources,
		fsCache:               fsCache,
		prefetchSize:          cfg.PrefetchSize,
		prefetchTimeout:       prefetchTimeout,
		noprefetch:            cfg.NoPrefetch,
		noBackgroundFetch:     cfg.NoBackgroundFetch,
		debug:                 cfg.Debug,
		layer:                 make(map[string]*layer),
		resolveResult:         lru.New(resolveResultEntry),
		blobResult:            lru.New(resolveResultEntry),
		backgroundTaskManager: task.NewBackgroundTaskManager(2, 5*time.Second),
		allowNoVerification:   cfg.AllowNoVerification,
		disableVerification:   cfg.DisableVerification,
	}, nil
}

type filesystem struct {
	resolver              *remote.Resolver
	fsCache               cache.BlobCache
	prefetchSize          int64
	prefetchTimeout       time.Duration
	noprefetch            bool
	noBackgroundFetch     bool
	debug                 bool
	layer                 map[string]*layer
	layerMu               sync.Mutex
	resolveResult         *lru.Cache
	resolveResultMu       sync.Mutex
	blobResult            *lru.Cache
	blobResultMu          sync.Mutex
	backgroundTaskManager *task.BackgroundTaskManager
	allowNoVerification   bool
	disableVerification   bool
	getSources            source.GetSources
	resolveG              singleflight.Group
}

func (fs *filesystem) Mount(ctx context.Context, mountpoint string, labels map[string]string) error {
	// This is a prioritized task and all background tasks will be stopped
	// execution so this can avoid being disturbed for NW traffic by background
	// tasks.
	fs.backgroundTaskManager.DoPrioritizedTask()
	defer fs.backgroundTaskManager.DonePrioritizedTask()
	ctx = log.WithLogger(ctx, log.G(ctx).WithField("mountpoint", mountpoint))

	// Get source information of this layer.
	src, err := fs.getSources(labels)
	if err != nil {
		return err
	} else if len(src) == 0 {
		return fmt.Errorf("source must be passed")
	}

	// Resolve the target layer
	var (
		resultChan = make(chan *layer)
		errChan    = make(chan error)
	)
	go func() {
		rErr := fmt.Errorf("failed to resolve target")
		for _, s := range src {
			l, err := fs.resolveLayer(ctx, s.Hosts, s.Name, s.Target)
			if err == nil {
				resultChan <- l
				return
			}
			rErr = errors.Wrapf(rErr, "failed to resolve layer %q from %q: %v",
				s.Target.Digest, s.Name, err)
		}
		errChan <- rErr
	}()

	// Also resolve other layers in parallel
	preResolve := src[0] // TODO: should we pre-resolve blobs in other sources as well?
	for _, desc := range preResolve.Manifest.Layers {
		if desc.Digest.String() != preResolve.Target.Digest.String() {
			go fs.resolveLayer(ctx, preResolve.Hosts, preResolve.Name, desc)
		}
	}

	// Wait for resolving completion
	var l *layer
	select {
	case l = <-resultChan:
	case err := <-errChan:
		log.G(ctx).WithError(err).Debug("failed to resolve layer")
		return errors.Wrapf(err, "failed to resolve layer")
	case <-time.After(30 * time.Second):
		log.G(ctx).Debug("failed to resolve layer (timeout)")
		return fmt.Errorf("failed to resolve layer (timeout)")
	}

	// Verify layer's content
	if fs.disableVerification {
		// Skip if verification is disabled completely
		l.skipVerify()
		log.G(ctx).Debugf("Verification forcefully skipped")
	} else if tocDigest, ok := labels[estargz.TOCJSONDigestAnnotation]; ok {
		// Verify this layer using the TOC JSON digest passed through label.
		dgst, err := digest.Parse(tocDigest)
		if err != nil {
			log.G(ctx).WithError(err).Debugf("failed to parse passed TOC digest %q", dgst)
			return errors.Wrapf(err, "invalid TOC digest: %v", tocDigest)
		}
		if err := l.verify(dgst); err != nil {
			log.G(ctx).WithError(err).Debugf("invalid layer")
			return errors.Wrapf(err, "invalid stargz layer")
		}
		log.G(ctx).Debugf("verified")
	} else if _, ok := labels[config.TargetSkipVerifyLabel]; ok && fs.allowNoVerification {
		// If unverified layer is allowed, use it with warning.
		// This mode is for legacy stargz archives which don't contain digests
		// necessary for layer verification.
		l.skipVerify()
		log.G(ctx).Warningf("No verification is held for layer")
	} else {
		// Verification must be done. Don't mount this layer.
		return fmt.Errorf("digest of TOC JSON must be passed")
	}
	layerReader, err := l.reader()
	if err != nil {
		log.G(ctx).WithError(err).Warningf("failed to get reader for layer")
		return err
	}

	// Register the mountpoint layer
	fs.layerMu.Lock()
	fs.layer[mountpoint] = l
	fs.layerMu.Unlock()

	// Prefetch this layer. We prefetch several layers in parallel. The first
	// Check() for this layer waits for the prefetch completion.
	if !fs.noprefetch {
		prefetchSize := fs.prefetchSize
		if psStr, ok := labels[config.TargetPrefetchSizeLabel]; ok {
			if ps, err := strconv.ParseInt(psStr, 10, 64); err == nil {
				prefetchSize = ps
			}
		}
		go func() {
			fs.backgroundTaskManager.DoPrioritizedTask()
			defer fs.backgroundTaskManager.DonePrioritizedTask()
			if err := l.prefetch(prefetchSize); err != nil {
				log.G(ctx).WithError(err).Debug("failed to prefetched layer")
				return
			}
			log.G(ctx).Debug("completed to prefetch")
		}()
	}

	// Fetch whole layer aggressively in background. We use background
	// reader for this so prioritized tasks(Mount, Check, etc...) can
	// interrupt the reading. This can avoid disturbing prioritized tasks
	// about NW traffic.
	if !fs.noBackgroundFetch {
		go func() {
			br := io.NewSectionReader(readerAtFunc(func(p []byte, offset int64) (retN int, retErr error) {
				fs.backgroundTaskManager.InvokeBackgroundTask(func(ctx context.Context) {
					retN, retErr = l.blob.ReadAt(
						p,
						offset,
						remote.WithContext(ctx),              // Make cancellable
						remote.WithCacheOpts(cache.Direct()), // Do not pollute mem cache
					)
				}, 120*time.Second)
				return
			}), 0, l.blob.Size())
			if err := layerReader.Cache(
				reader.WithReader(br),                // Read contents in background
				reader.WithCacheOpts(cache.Direct()), // Do not pollute mem cache
			); err != nil {
				log.G(ctx).WithError(err).Debug("failed to fetch whole layer")
				return
			}
			log.G(ctx).Debug("completed to fetch all layer data in background")
		}()
	}

	// Mounting stargz
	// TODO: bind mount the state directory as a read-only fs on snapshotter's side
	timeSec := time.Second
	rawFS := fusefs.NewNodeFS(&node{
		fs:    fs,
		layer: layerReader,
		e:     l.root,
		s:     newState(l.desc.Digest.String(), l.blob),
		root:  mountpoint,
	}, &fusefs.Options{
		AttrTimeout:     &timeSec,
		EntryTimeout:    &timeSec,
		NullPermissions: true,
	})
	server, err := fuse.NewServer(rawFS, mountpoint, &fuse.MountOptions{
		AllowOther: true,             // allow users other than root&mounter to access fs
		FsName:     "stargz",         // name this filesystem as "stargz"
		Options:    []string{"suid"}, // allow setuid inside container
		Debug:      fs.debug,
	})
	if err != nil {
		log.G(ctx).WithError(err).Debug("failed to make filesstem server")
		return err
	}

	go server.Serve()
	return server.WaitMount()
}

func (fs *filesystem) resolveLayer(ctx context.Context, hosts docker.RegistryHosts, refspec reference.Spec, desc ocispec.Descriptor) (*layer, error) {
	name := refspec.String() + "/" + desc.Digest.String()
	ctx, cancel := context.WithCancel(log.WithLogger(ctx, log.G(ctx).WithField("src", name)))
	defer cancel()

	fs.resolveResultMu.Lock()
	c, ok := fs.resolveResult.Get(name)
	fs.resolveResultMu.Unlock()
	if ok && c.(*layer).blob.Check() == nil {
		return c.(*layer), nil
	}

	resultChan := fs.resolveG.DoChan(name, func() (interface{}, error) {
		log.G(ctx).Debugf("resolving")

		// Resolve the blob. The result will be cached for future use. This is effective
		// in some failure cases including resolving is succeeded but the blob is non-stargz.
		var blob remote.Blob
		fs.blobResultMu.Lock()
		c, ok := fs.blobResult.Get(name)
		fs.blobResultMu.Unlock()
		if ok && c.(remote.Blob).Check() == nil {
			blob = c.(remote.Blob)
		} else {
			var err error
			blob, err = fs.resolver.Resolve(ctx, hosts, refspec, desc)
			if err != nil {
				log.G(ctx).WithError(err).Debugf("failed to resolve source")
				return nil, errors.Wrap(err, "failed to resolve the source")
			}
			fs.blobResultMu.Lock()
			fs.blobResult.Add(name, blob)
			fs.blobResultMu.Unlock()
		}

		// Get a reader for stargz archive.
		// Each file's read operation is a prioritized task and all background tasks
		// will be stopped during the execution so this can avoid being disturbed for
		// NW traffic by background tasks.
		sr := io.NewSectionReader(readerAtFunc(func(p []byte, offset int64) (n int, err error) {
			fs.backgroundTaskManager.DoPrioritizedTask()
			defer fs.backgroundTaskManager.DonePrioritizedTask()
			return blob.ReadAt(p, offset)
		}), 0, blob.Size())
		vr, root, err := reader.NewReader(sr, fs.fsCache)
		if err != nil {
			log.G(ctx).WithError(err).Debugf("failed to resolve: layer cannot be read")
			return nil, errors.Wrap(err, "failed to read layer")
		}

		// Combine layer information together
		l := newLayer(desc, blob, vr, root, fs.prefetchTimeout)
		fs.resolveResultMu.Lock()
		fs.resolveResult.Add(name, l)
		fs.resolveResultMu.Unlock()

		log.G(ctx).Debugf("resolved")
		return l, nil
	})

	var res singleflight.Result
	select {
	case res = <-resultChan:
	case <-time.After(30 * time.Second):
		fs.resolveG.Forget(name)
		return nil, fmt.Errorf("failed to resolve layer (timeout)")
	}
	if res.Err != nil || res.Val == nil {
		return nil, fmt.Errorf("failed to resolve layer: %v", res.Err)
	}
	return res.Val.(*layer), nil
}

func (fs *filesystem) Check(ctx context.Context, mountpoint string, labels map[string]string) error {
	// This is a prioritized task and all background tasks will be stopped
	// execution so this can avoid being disturbed for NW traffic by background
	// tasks.
	fs.backgroundTaskManager.DoPrioritizedTask()
	defer fs.backgroundTaskManager.DonePrioritizedTask()

	ctx = log.WithLogger(ctx, log.G(ctx).WithField("mountpoint", mountpoint))

	fs.layerMu.Lock()
	l := fs.layer[mountpoint]
	fs.layerMu.Unlock()
	if l == nil {
		log.G(ctx).Debug("layer not registered")
		return fmt.Errorf("layer not registered")
	}

	// Check the blob connectivity and try to refresh the connection on failure
	if err := fs.check(ctx, l, labels); err != nil {
		log.G(ctx).WithError(err).Warn("check failed")
		return err
	}

	// Wait for prefetch compeletion
	if !fs.noprefetch {
		if err := l.waitForPrefetchCompletion(); err != nil {
			log.G(ctx).WithError(err).Warn("failed to sync with prefetch completion")
		}
	}

	return nil
}

func (fs *filesystem) check(ctx context.Context, l *layer, labels map[string]string) error {
	err := l.blob.Check()
	if err == nil {
		return nil
	}
	log.G(ctx).WithError(err).Warn("failed to connect to blob")

	// Check failed. Try to refresh the connection with fresh source information
	src, err := fs.getSources(labels)
	if err != nil {
		return err
	}
	var (
		retrynum = 1
		rErr     = fmt.Errorf("failed to refresh connection")
	)
	for retry := 0; retry < retrynum; retry++ {
		log.G(ctx).Warnf("refreshing(%d)...", retry)
		for _, s := range src {
			err := l.blob.Refresh(ctx, s.Hosts, s.Name, s.Target)
			if err == nil {
				log.G(ctx).Debug("Successfully refreshed connection")
				return nil
			}
			log.G(ctx).WithError(err).Warnf("failed to refresh the layer %q from %q",
				s.Target.Digest, s.Name)
			rErr = errors.Wrapf(rErr, "failed(layer:%q, ref:%q): %v",
				s.Target.Digest, s.Name, err)
		}
	}

	return rErr
}

func (fs *filesystem) Unmount(ctx context.Context, mountpoint string) error {
	fs.layerMu.Lock()
	if _, ok := fs.layer[mountpoint]; !ok {
		fs.layerMu.Unlock()
		return fmt.Errorf("specified path %q isn't a mountpoint", mountpoint)
	}
	delete(fs.layer, mountpoint) // unregisters the corresponding layer
	fs.layerMu.Unlock()
	// The goroutine which serving the mountpoint possibly becomes not responding.
	// In case of such situations, we use MNT_FORCE here and abort the connection.
	// In the future, we might be able to consider to kill that specific hanging
	// goroutine using channel, etc.
	// See also: https://www.kernel.org/doc/html/latest/filesystems/fuse.html#aborting-a-filesystem-connection
	return syscall.Unmount(mountpoint, syscall.MNT_FORCE)
}

func newLayer(desc ocispec.Descriptor, blob remote.Blob, vr *reader.VerifiableReader, root *estargz.TOCEntry, prefetchTimeout time.Duration) *layer {
	return &layer{
		desc:             desc,
		blob:             blob,
		verifiableReader: vr,
		root:             root,
		prefetchWaiter:   newWaiter(),
		prefetchTimeout:  prefetchTimeout,
	}
}

type layer struct {
	desc             ocispec.Descriptor
	blob             remote.Blob
	verifiableReader *reader.VerifiableReader
	root             *estargz.TOCEntry
	prefetchWaiter   *waiter
	prefetchTimeout  time.Duration
	r                reader.Reader
}

func (l *layer) reader() (reader.Reader, error) {
	if l.r == nil {
		return nil, fmt.Errorf("layer hasn't been verified yet")
	}
	return l.r, nil
}

func (l *layer) skipVerify() {
	l.r = l.verifiableReader.SkipVerify()
}

func (l *layer) verify(tocDigest digest.Digest) (err error) {
	l.r, err = l.verifiableReader.VerifyTOC(tocDigest)
	return
}

func (l *layer) prefetch(prefetchSize int64) error {
	defer l.prefetchWaiter.done() // Notify the completion

	lr, err := l.reader()
	if err != nil {
		return err
	}
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

func (l *layer) waitForPrefetchCompletion() error {
	return l.prefetchWaiter.wait(l.prefetchTimeout)
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

type fileReader interface {
	OpenFile(name string) (io.ReaderAt, error)
}

// node is a filesystem inode abstraction.
type node struct {
	fusefs.Inode
	fs     *filesystem
	layer  fileReader
	e      *estargz.TOCEntry
	s      *state
	root   string
	opaque bool // true if this node is an overlayfs opaque directory
}

var _ = (fusefs.InodeEmbedder)((*node)(nil))

var _ = (fusefs.NodeReaddirer)((*node)(nil))

func (n *node) Readdir(ctx context.Context) (fusefs.DirStream, syscall.Errno) {
	var ents []fuse.DirEntry
	whiteouts := map[string]*estargz.TOCEntry{}
	normalEnts := map[string]bool{}
	n.e.ForeachChild(func(baseName string, ent *estargz.TOCEntry) bool {

		// We don't want to show prefetch landmarks in "/".
		if n.e.Name == "" && (baseName == estargz.PrefetchLandmark || baseName == estargz.NoPrefetchLandmark) {
			return true
		}

		// We don't want to show whiteouts.
		if strings.HasPrefix(baseName, whiteoutPrefix) {
			if baseName == whiteoutOpaqueDir {
				return true
			}
			// Add the overlayfs-compiant whiteout later.
			whiteouts[baseName] = ent
			return true
		}

		// This is a normal entry.
		normalEnts[baseName] = true
		ents = append(ents, fuse.DirEntry{
			Mode: modeOfEntry(ent),
			Name: baseName,
			Ino:  inodeOfEnt(ent),
		})
		return true
	})

	// Append whiteouts if no entry replaces the target entry in the lower layer.
	for w, ent := range whiteouts {
		if !normalEnts[w[len(whiteoutPrefix):]] {
			ents = append(ents, fuse.DirEntry{
				Mode: syscall.S_IFCHR,
				Name: w[len(whiteoutPrefix):],
				Ino:  inodeOfEnt(ent),
			})

		}
	}

	return fusefs.NewListDirStream(ents), 0
}

var _ = (fusefs.NodeLookuper)((*node)(nil))

func (n *node) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fusefs.Inode, syscall.Errno) {
	// We don't want to show prefetch landmarks in "/".
	if n.e.Name == "" && (name == estargz.PrefetchLandmark || name == estargz.NoPrefetchLandmark) {
		return nil, syscall.ENOENT
	}

	// We don't want to show whiteouts.
	if strings.HasPrefix(name, whiteoutPrefix) {
		return nil, syscall.ENOENT
	}

	// state directory
	if n.e.Name == "" && name == stateDirName {
		return n.NewInode(ctx, n.s, stateToAttr(n.s, &out.Attr)), 0
	}

	// lookup stargz TOCEntry
	ce, ok := n.e.LookupChild(name)
	if !ok {
		// If the entry exists as a whiteout, show an overlayfs-styled whiteout node.
		if wh, ok := n.e.LookupChild(fmt.Sprintf("%s%s", whiteoutPrefix, name)); ok {
			return n.NewInode(ctx, &whiteout{
				e: wh,
			}, entryToWhAttr(wh, &out.Attr)), 0
		}
		return nil, syscall.ENOENT
	}
	var opaque bool
	if _, ok := ce.LookupChild(whiteoutOpaqueDir); ok {
		// This entry is an opaque directory so make it recognizable for overlayfs.
		opaque = true
	}

	return n.NewInode(ctx, &node{
		fs:     n.fs,
		layer:  n.layer,
		e:      ce,
		s:      n.s,
		root:   n.root,
		opaque: opaque,
	}, entryToAttr(ce, &out.Attr)), 0
}

var _ = (fusefs.NodeOpener)((*node)(nil))

func (n *node) Open(ctx context.Context, flags uint32) (fh fusefs.FileHandle, fuseFlags uint32, errno syscall.Errno) {
	ra, err := n.layer.OpenFile(n.e.Name)
	if err != nil {
		n.s.report(fmt.Errorf("failed to open node: %v", err))
		return nil, 0, syscall.EIO
	}
	return &file{
		n:  n,
		e:  n.e,
		ra: ra,
	}, 0, 0
}

var _ = (fusefs.NodeGetattrer)((*node)(nil))

func (n *node) Getattr(ctx context.Context, f fusefs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	entryToAttr(n.e, &out.Attr)
	return 0
}

var _ = (fusefs.NodeGetxattrer)((*node)(nil))

func (n *node) Getxattr(ctx context.Context, attr string, dest []byte) (uint32, syscall.Errno) {
	if attr == opaqueXattr && n.opaque {
		// This node is an opaque directory so give overlayfs-compliant indicator.
		if len(dest) < len(opaqueXattrValue) {
			return uint32(len(opaqueXattrValue)), syscall.ERANGE
		}
		return uint32(copy(dest, opaqueXattrValue)), 0
	}
	if v, ok := n.e.Xattrs[attr]; ok {
		if len(dest) < len(v) {
			return uint32(len(v)), syscall.ERANGE
		}
		return uint32(copy(dest, v)), 0
	}
	return 0, syscall.ENODATA
}

var _ = (fusefs.NodeListxattrer)((*node)(nil))

func (n *node) Listxattr(ctx context.Context, dest []byte) (uint32, syscall.Errno) {
	var attrs []byte
	if n.opaque {
		// This node is an opaque directory so add overlayfs-compliant indicator.
		attrs = append(attrs, []byte(opaqueXattr+"\x00")...)
	}
	for k := range n.e.Xattrs {
		attrs = append(attrs, []byte(k+"\x00")...)
	}
	if len(dest) < len(attrs) {
		return uint32(len(attrs)), syscall.ERANGE
	}
	return uint32(copy(dest, attrs)), 0
}

var _ = (fusefs.NodeReadlinker)((*node)(nil))

func (n *node) Readlink(ctx context.Context) ([]byte, syscall.Errno) {
	return []byte(n.e.LinkName), 0
}

var _ = (fusefs.NodeStatfser)((*node)(nil))

func (n *node) Statfs(ctx context.Context, out *fuse.StatfsOut) syscall.Errno {
	defaultStatfs(out)
	return 0
}

// file is a file abstraction which implements file handle in go-fuse.
type file struct {
	n  *node
	e  *estargz.TOCEntry
	ra io.ReaderAt
}

var _ = (fusefs.FileReader)((*file)(nil))

func (f *file) Read(ctx context.Context, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	n, err := f.ra.ReadAt(dest, off)
	if err != nil && err != io.EOF {
		f.n.s.report(fmt.Errorf("failed to read node: %v", err))
		return nil, syscall.EIO
	}
	return fuse.ReadResultData(dest[:n]), 0
}

var _ = (fusefs.FileGetattrer)((*file)(nil))

func (f *file) Getattr(ctx context.Context, out *fuse.AttrOut) syscall.Errno {
	entryToAttr(f.e, &out.Attr)
	return 0
}

// whiteout is a whiteout abstraction compliant to overlayfs.
type whiteout struct {
	fusefs.Inode
	e *estargz.TOCEntry
}

var _ = (fusefs.NodeGetattrer)((*whiteout)(nil))

func (w *whiteout) Getattr(ctx context.Context, f fusefs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	entryToWhAttr(w.e, &out.Attr)
	return 0
}

var _ = (fusefs.NodeStatfser)((*whiteout)(nil))

func (w *whiteout) Statfs(ctx context.Context, out *fuse.StatfsOut) syscall.Errno {
	defaultStatfs(out)
	return 0
}

// newState provides new state directory node.
// It creates statFile at the same time to give it stable inode number.
func newState(digest string, blob remote.Blob) *state {
	return &state{
		statFile: &statFile{
			name: digest + ".json",
			statJSON: statJSON{
				Digest: digest,
				Size:   blob.Size(),
			},
			blob: blob,
		},
	}
}

// state is a directory which contain a "state file" of this layer aming to
// observability. This filesystem uses it to report something(e.g. error) to
// the clients(e.g. Kubernetes's livenessProbe).
// This directory has mode "dr-x------ root root".
type state struct {
	fusefs.Inode
	statFile *statFile
}

var _ = (fusefs.NodeReaddirer)((*state)(nil))

func (s *state) Readdir(ctx context.Context) (fusefs.DirStream, syscall.Errno) {
	return fusefs.NewListDirStream([]fuse.DirEntry{
		{
			Mode: statFileMode,
			Name: s.statFile.name,
			Ino:  inodeOfStatFile(s.statFile),
		},
	}), 0
}

var _ = (fusefs.NodeLookuper)((*state)(nil))

func (s *state) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fusefs.Inode, syscall.Errno) {
	if name != s.statFile.name {
		return nil, syscall.ENOENT
	}
	attr, errno := s.statFile.attr(&out.Attr)
	if errno != 0 {
		return nil, errno
	}
	return s.NewInode(ctx, s.statFile, attr), 0
}

var _ = (fusefs.NodeGetattrer)((*state)(nil))

func (s *state) Getattr(ctx context.Context, f fusefs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	stateToAttr(s, &out.Attr)
	return 0
}

var _ = (fusefs.NodeStatfser)((*state)(nil))

func (s *state) Statfs(ctx context.Context, out *fuse.StatfsOut) syscall.Errno {
	defaultStatfs(out)
	return 0
}

func (s *state) report(err error) {
	s.statFile.report(err)
}

type statJSON struct {
	Error  string `json:"error,omitempty"`
	Digest string `json:"digest"`
	// URL is excluded for potential security reason
	Size           int64   `json:"size"`
	FetchedSize    int64   `json:"fetchedSize"`
	FetchedPercent float64 `json:"fetchedPercent"` // Fetched / Size * 100.0
}

// statFile is a file which contain something to be reported from this layer.
// This filesystem uses statFile.report() to report something(e.g. error) to
// the clients(e.g. Kubernetes's livenessProbe).
// This file has mode "-r-------- root root".
type statFile struct {
	fusefs.Inode
	name     string
	blob     remote.Blob
	statJSON statJSON
	mu       sync.Mutex
}

var _ = (fusefs.NodeOpener)((*statFile)(nil))

func (sf *statFile) Open(ctx context.Context, flags uint32) (fh fusefs.FileHandle, fuseFlags uint32, errno syscall.Errno) {
	return nil, 0, 0
}

var _ = (fusefs.NodeReader)((*statFile)(nil))

func (sf *statFile) Read(ctx context.Context, f fusefs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	st, err := sf.updateStatUnlocked()
	if err != nil {
		return nil, syscall.EIO
	}
	n, err := bytes.NewReader(st).ReadAt(dest, off)
	if err != nil && err != io.EOF {
		return nil, syscall.EIO
	}
	return fuse.ReadResultData(dest[:n]), 0
}

var _ = (fusefs.NodeGetattrer)((*statFile)(nil))

func (sf *statFile) Getattr(ctx context.Context, f fusefs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	_, errno := sf.attr(&out.Attr)
	return errno
}

var _ = (fusefs.NodeStatfser)((*statFile)(nil))

func (sf *statFile) Statfs(ctx context.Context, out *fuse.StatfsOut) syscall.Errno {
	defaultStatfs(out)
	return 0
}

func (sf *statFile) report(err error) {
	sf.mu.Lock()
	defer sf.mu.Unlock()
	sf.statJSON.Error = err.Error()
}

func (sf *statFile) attr(out *fuse.Attr) (fusefs.StableAttr, syscall.Errno) {
	sf.mu.Lock()
	defer sf.mu.Unlock()

	st, err := sf.updateStatUnlocked()
	if err != nil {
		return fusefs.StableAttr{}, syscall.EIO
	}

	return statFileToAttr(sf, uint64(len(st)), out), 0
}

func (sf *statFile) updateStatUnlocked() ([]byte, error) {
	sf.statJSON.FetchedSize = sf.blob.FetchedSize()
	sf.statJSON.FetchedPercent = float64(sf.statJSON.FetchedSize) / float64(sf.statJSON.Size) * 100.0
	j, err := json.Marshal(&sf.statJSON)
	if err != nil {
		return nil, err
	}
	j = append(j, []byte("\n")...)
	return j, nil
}

// inodeOfEnt calculates the inode number which is one-to-one conresspondence
// with the TOCEntry insntance.
func inodeOfEnt(e *estargz.TOCEntry) uint64 {
	return uint64(uintptr(unsafe.Pointer(e)))
}

// entryToAttr converts stargz's TOCEntry to go-fuse's Attr.
func entryToAttr(e *estargz.TOCEntry, out *fuse.Attr) fusefs.StableAttr {
	out.Ino = inodeOfEnt(e)
	out.Size = uint64(e.Size)
	out.Blksize = blockSize
	out.Blocks = out.Size / uint64(out.Blksize)
	if out.Size%uint64(out.Blksize) > 0 {
		out.Blocks++
	}
	mtime := e.ModTime()
	out.SetTimes(nil, &mtime, nil)
	out.Mode = modeOfEntry(e)
	out.Owner = fuse.Owner{Uid: uint32(e.UID), Gid: uint32(e.GID)}
	out.Rdev = uint32(unix.Mkdev(uint32(e.DevMajor), uint32(e.DevMinor)))
	out.Nlink = uint32(e.NumLink)
	if out.Nlink == 0 {
		out.Nlink = 1 // zero "NumLink" means one.
	}
	out.Padding = 0 // TODO

	return fusefs.StableAttr{
		Mode: out.Mode,
		Ino:  out.Ino,
		// NOTE: The inode number is unique throughout the lifettime of
		// this filesystem so we don't consider about generation at this
		// moment.
	}
}

// entryToWhAttr converts stargz's TOCEntry to go-fuse's Attr of whiteouts.
func entryToWhAttr(e *estargz.TOCEntry, out *fuse.Attr) fusefs.StableAttr {
	fi := e.Stat()
	out.Ino = inodeOfEnt(e)
	out.Size = 0
	out.Blksize = blockSize
	out.Blocks = 0
	mtime := fi.ModTime()
	out.SetTimes(nil, &mtime, nil)
	out.Mode = syscall.S_IFCHR
	out.Owner = fuse.Owner{Uid: 0, Gid: 0}
	out.Rdev = uint32(unix.Mkdev(0, 0))
	out.Nlink = 1
	out.Padding = 0 // TODO

	return fusefs.StableAttr{
		Mode: out.Mode,
		Ino:  out.Ino,
		// NOTE: The inode number is unique throughout the lifettime of
		// this filesystem so we don't consider about generation at this
		// moment.
	}
}

// inodeOfState calculates the inode number which is one-to-one conresspondence
// with the state directory insntance which was created on mount.
func inodeOfState(s *state) uint64 {
	return uint64(uintptr(unsafe.Pointer(s)))
}

// stateToAttr converts state directory to go-fuse's Attr.
func stateToAttr(s *state, out *fuse.Attr) fusefs.StableAttr {
	out.Ino = inodeOfState(s)
	out.Size = 0
	out.Blksize = blockSize
	out.Blocks = 0
	out.Nlink = 1

	// root can read and open it (dr-x------ root root).
	out.Mode = stateDirMode
	out.Owner = fuse.Owner{Uid: 0, Gid: 0}

	// dummy
	out.Mtime = 0
	out.Mtimensec = 0
	out.Rdev = 0
	out.Padding = 0

	return fusefs.StableAttr{
		Mode: out.Mode,
		Ino:  out.Ino,
		// NOTE: The inode number is unique throughout the lifettime of
		// this filesystem so we don't consider about generation at this
		// moment.
	}
}

// inodeOfStatFile calculates the inode number which is one-to-one conresspondence
// with the stat file insntance which was created on mount.
func inodeOfStatFile(s *statFile) uint64 {
	return uint64(uintptr(unsafe.Pointer(s)))
}

// statFileToAttr converts stat file to go-fuse's Attr.
func statFileToAttr(sf *statFile, size uint64, out *fuse.Attr) fusefs.StableAttr {
	out.Ino = inodeOfStatFile(sf)
	out.Size = size
	out.Blksize = blockSize
	out.Blocks = out.Size / uint64(out.Blksize)
	out.Nlink = 1

	// Root can read it ("-r-------- root root").
	out.Mode = statFileMode
	out.Owner = fuse.Owner{Uid: 0, Gid: 0}

	// dummy
	out.Mtime = 0
	out.Mtimensec = 0
	out.Rdev = 0
	out.Padding = 0

	return fusefs.StableAttr{
		Mode: out.Mode,
		Ino:  out.Ino,
		// NOTE: The inode number is unique throughout the lifettime of
		// this filesystem so we don't consider about generation at this
		// moment.
	}
}

// modeOfEntry gets system's mode bits from TOCEntry
func modeOfEntry(e *estargz.TOCEntry) uint32 {
	// Permission bits
	res := uint32(e.Stat().Mode() & os.ModePerm)

	// File type bits
	switch e.Stat().Mode() & os.ModeType {
	case os.ModeDevice:
		res |= syscall.S_IFBLK
	case os.ModeDevice | os.ModeCharDevice:
		res |= syscall.S_IFCHR
	case os.ModeDir:
		res |= syscall.S_IFDIR
	case os.ModeNamedPipe:
		res |= syscall.S_IFIFO
	case os.ModeSymlink:
		res |= syscall.S_IFLNK
	case os.ModeSocket:
		res |= syscall.S_IFSOCK
	default: // regular file.
		res |= syscall.S_IFREG
	}

	// SUID, SGID, Sticky bits
	// Stargz package doesn't provide these bits so let's calculate them manually
	// here. TOCEntry.Mode is a copy of tar.Header.Mode so we can understand the
	// mode using that package.
	// See also:
	// - https://github.com/google/crfs/blob/71d77da419c90be7b05d12e59945ac7a8c94a543/stargz/stargz.go#L706
	hm := (&tar.Header{Mode: e.Mode}).FileInfo().Mode()
	if hm&os.ModeSetuid != 0 {
		res |= syscall.S_ISUID
	}
	if hm&os.ModeSetgid != 0 {
		res |= syscall.S_ISGID
	}
	if hm&os.ModeSticky != 0 {
		res |= syscall.S_ISVTX
	}

	return res
}

func defaultStatfs(stat *fuse.StatfsOut) {

	// http://man7.org/linux/man-pages/man2/statfs.2.html
	stat.Blocks = 0 // dummy
	stat.Bfree = 0
	stat.Bavail = 0
	stat.Files = 0 // dummy
	stat.Ffree = 0
	stat.Bsize = blockSize
	stat.NameLen = 1<<32 - 1
	stat.Frsize = blockSize
	stat.Padding = 0
	stat.Spare = [6]uint32{}
}
