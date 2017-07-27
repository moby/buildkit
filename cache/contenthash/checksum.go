package contenthash

import (
	"bytes"
	"context"
	"crypto/sha256"
	"os"
	"path"
	"path/filepath"
	"sync"

	"github.com/docker/docker/pkg/pools"
	"github.com/docker/docker/pkg/symlink"
	iradix "github.com/hashicorp/go-immutable-radix"
	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/snapshot"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
	"github.com/tonistiigi/fsutil"
)

var errNotFound = errors.Errorf("not found")

var defaultManager = &cacheManager{actives: map[string]*cacheContext{}}

// Layout in the radix tree: Every path is saved by cleaned absolute unix path.
// Directories have 2 records, one contains digest for directory header, other
// the recursive digest for directory contents. "/dir/" is the record for
// header, "/dir" is for contents. For the root node "" (empty string) is the
// key for root, "/" for the root header

func Checksum(ctx context.Context, ref cache.ImmutableRef, path string) (digest.Digest, error) {
	return defaultManager.Checksum(ctx, ref, path)
}

// func GetCacheContext(ctx context.Context, ref cache.ImmutableRef) (CacheContext, error) {
//
// }
//
// func SetCacheContext(ctx context.Context, ref cache.ImmutableRef, cc CacheContext) error {
//
// }

type CacheContext interface {
	HandleChange(kind fsutil.ChangeKind, p string, fi os.FileInfo, err error)
	// Reset(p string)
	Marshal() ([]byte, error)
}

type CacheRecord struct {
	Type   CacheRecordType
	Link   string
	Digest digest.Digest
}

type Hashed interface {
	Digest() digest.Digest
}

type CacheRecordType int

const (
	CacheRecordFile CacheRecordType = iota
	CacheRecordDir
	CacheRecordDirHeader
	CacheRecordSymlink
)

type cacheManager struct {
	mu      sync.Mutex
	actives map[string]*cacheContext
}

func (cm *cacheManager) Checksum(ctx context.Context, ref cache.ImmutableRef, p string) (digest.Digest, error) {
	cm.mu.Lock()
	cc, ok := cm.actives[ref.ID()]
	if !ok {
		cc = newCacheContext(ref)
		cm.actives[ref.ID()] = cc
	}
	cc.refs++
	cm.mu.Unlock()

	defer func() {
		cm.mu.Lock()
		cc.refs--
		if cc.refs == 0 {
			cc.save() // TODO: do this on background, BUT need to unmount before releasing, possibly wrap ref
			cc.clean()
			delete(cm.actives, ref.ID())
		}
		cm.mu.Unlock()
	}()

	return cc.Checksum(ctx, p)
}

type cacheContext struct {
	mu        sync.RWMutex
	mountPath string
	unmount   func() error
	ref       cache.ImmutableRef
	refs      int
	tree      *iradix.Tree
	// isDirty   bool

	// used in HandleChange
	txn      *iradix.Txn
	node     *iradix.Node
	dirtyMap map[string]struct{}
}

func newCacheContext(ref cache.ImmutableRef) *cacheContext {
	cc := &cacheContext{
		ref:      ref,
		tree:     iradix.New(),
		dirtyMap: map[string]struct{}{},
	}
	// cc.Load(md)
	return cc
}

func (cc *cacheContext) save() {
	// TODO:
}

// HandleChange notifies the source about a modification operation
func (cc *cacheContext) HandleChange(kind fsutil.ChangeKind, p string, fi os.FileInfo, err error) (retErr error) {
	p = path.Join("/", filepath.ToSlash(p))
	if p == "/" {
		p = ""
	}
	k := []byte(p)

	deleteDir := func(cr *CacheRecord) {
		if cr.Type == CacheRecordDir {
			cc.node.WalkPrefix(append(k, []byte("/")...), func(k []byte, v interface{}) bool {
				cc.txn.Delete(k)
				return false
			})
		}
	}

	cc.mu.Lock()
	if cc.txn == nil {
		cc.txn = cc.tree.Txn()
		cc.node = cc.tree.Root()
	}
	if kind == fsutil.ChangeKindDelete {
		v, ok := cc.txn.Delete(k)
		if ok {
			deleteDir(v.(*CacheRecord))
		}
		d := path.Dir(string(k))
		if d == "/" {
			d = ""
		}
		cc.dirtyMap[d] = struct{}{}
		cc.mu.Unlock()
		return
	}

	stat, ok := fi.Sys().(*fsutil.Stat)
	if !ok {
		return errors.Errorf("%s invalid change without stat information", p)
	}

	h, ok := fi.(Hashed)
	if !ok {
		cc.mu.Unlock()
		return errors.Errorf("invalid fileinfo: %s", p)
	}

	v, ok := cc.node.Get(k)
	if ok {
		deleteDir(v.(*CacheRecord))
	}

	cr := &CacheRecord{
		Type: CacheRecordFile,
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		cr.Type = CacheRecordSymlink
		cr.Link = filepath.ToSlash(stat.Linkname)
	}
	if fi.IsDir() {
		cr.Type = CacheRecordDirHeader
		cr2 := &CacheRecord{
			Type: CacheRecordDir,
		}
		cc.txn.Insert(k, cr2)
		k = append(k, []byte("/")...)
	}
	cr.Digest = h.Digest()
	cc.txn.Insert(k, cr)
	d := path.Dir(string(k))
	if d == "/" {
		d = ""
	}
	cc.dirtyMap[d] = struct{}{}
	cc.mu.Unlock()

	return nil
}

func (cc *cacheContext) Checksum(ctx context.Context, p string) (digest.Digest, error) {
	const maxSymlinkLimit = 255
	i := 0
	for {
		if i > maxSymlinkLimit {
			return "", errors.Errorf("too many symlinks: %s", p)
		}
		cr, err := cc.ChecksumNoFollow(ctx, p)
		if err != nil {
			return "", err
		}
		if cr.Type == CacheRecordSymlink {
			link := cr.Link
			if !path.IsAbs(cr.Link) {
				link = path.Join(path.Dir(p), link)
			}
			i++
			p = link
		} else {
			return cr.Digest, nil
		}
	}
}

func (cc *cacheContext) ChecksumNoFollow(ctx context.Context, p string) (*CacheRecord, error) {
	p = path.Join("/", filepath.ToSlash(p))
	if p == "/" {
		p = ""
	}

	cc.mu.RLock()
	if cc.txn == nil {
		root := cc.tree.Root()
		cc.mu.RUnlock()
		v, ok := root.Get([]byte(p))
		if ok {
			cr := v.(*CacheRecord)
			if cr.Digest != "" {
				return cr, nil
			}
		}
	} else {
		cc.mu.RUnlock()
	}

	cc.mu.Lock()
	defer cc.mu.Unlock()

	if cc.txn != nil {
		cc.commitActiveTransaction()
	}

	return cc.lazyChecksum(ctx, p)
}

func (cc *cacheContext) commitActiveTransaction() {
	for d := range cc.dirtyMap {
		addParentToMap(d, cc.dirtyMap)
	}
	for d := range cc.dirtyMap {
		cc.txn.Insert([]byte(d), &CacheRecord{Type: CacheRecordDir})
	}
	cc.tree = cc.txn.Commit()
	cc.node = nil
	cc.dirtyMap = map[string]struct{}{}
	cc.txn = nil
}

func (cc *cacheContext) lazyChecksum(ctx context.Context, p string) (*CacheRecord, error) {
	root := cc.tree.Root()
	if cc.needsScan(root, p) {
		if err := cc.scanPath(ctx, p); err != nil {
			return nil, err
		}
	}
	k := []byte(p)
	root = cc.tree.Root()
	txn := cc.tree.Txn()
	cr, err := cc.checksum(ctx, root, txn, k)
	if err != nil {
		return nil, err
	}
	cc.tree = txn.Commit()
	return cr, err
}

func (cc *cacheContext) checksum(ctx context.Context, root *iradix.Node, txn *iradix.Txn, k []byte) (*CacheRecord, error) {
	v, ok := root.Get(k)

	if !ok {
		return nil, errors.Wrapf(errNotFound, "%s not found", string(k))
	}
	cr := v.(*CacheRecord)

	if cr.Digest != "" {
		return cr, nil
	}
	var dgst digest.Digest

	switch cr.Type {
	case CacheRecordDir:
		h := sha256.New()
		iter := root.Iterator()
		next := append(k, []byte("/")...)
		iter.SeekPrefix(next)
		for {
			subk, _, ok := iter.Next()
			if !ok || bytes.Compare(next, subk) > 0 {
				break
			}
			h.Write(bytes.TrimPrefix(subk, k))

			subcr, err := cc.checksum(ctx, root, txn, subk)
			if err != nil {
				return nil, err
			}

			h.Write([]byte(subcr.Digest))
			if subcr.Type == CacheRecordDir { // skip subfiles
				next = append(k, []byte("/\xff")...)
				iter.SeekPrefix(next)
			}
		}
		dgst = digest.NewDigest(digest.SHA256, h)
	default:
		p := string(bytes.TrimSuffix(k, []byte("/")))

		target, err := cc.root(ctx)
		if err != nil {
			return nil, err
		}

		// no FollowSymlinkInScope because invalid paths should not be inserted
		fp := filepath.Join(target, filepath.FromSlash(p))

		fi, err := os.Lstat(fp)
		if err != nil {
			return nil, err
		}

		dgst, err = prepareDigest(fp, p, fi)
		if err != nil {
			return nil, err
		}
	}

	cr2 := &CacheRecord{
		Digest: dgst,
		Type:   cr.Type,
		Link:   cr.Link,
	}

	txn.Insert(k, cr2)

	return cr2, nil
}

func (cc *cacheContext) needsScan(root *iradix.Node, p string) bool {
	if p == "/" {
		p = ""
	}
	if _, ok := root.Get([]byte(p)); !ok {
		if p == "" {
			return true
		}
		return cc.needsScan(root, path.Clean(path.Dir(p)))
	}
	return false
}

func (cc *cacheContext) root(ctx context.Context) (string, error) {
	if cc.mountPath != "" {
		return cc.mountPath, nil
	}
	mounts, err := cc.ref.Mount(ctx, true)
	if err != nil {
		return "", err
	}

	lm := snapshot.LocalMounter(mounts)

	mp, err := lm.Mount()
	if err != nil {
		return "", err
	}

	cc.mountPath = mp
	cc.unmount = lm.Unmount
	return mp, nil
}

func (cc *cacheContext) clean() error {
	if cc.mountPath != "" {
		err := cc.unmount()
		cc.mountPath = ""
		cc.unmount = nil
		return err
	}
	return nil
}

func (cc *cacheContext) scanPath(ctx context.Context, p string) (retErr error) {
	p = path.Join("/", p)
	d, _ := path.Split(p)

	mp, err := cc.root(ctx)
	if err != nil {
		return err
	}

	parentPath, err := symlink.FollowSymlinkInScope(filepath.Join(mp, filepath.FromSlash(d)), mp)
	if err != nil {
		return err
	}

	n := cc.tree.Root()
	txn := cc.tree.Txn()

	err = filepath.Walk(parentPath, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return errors.Wrapf(err, "failed to walk %s", path)
		}
		rel, err := filepath.Rel(mp, path)
		if err != nil {
			return err
		}
		k := []byte(filepath.Join("/", filepath.ToSlash(rel)))
		if string(k) == "/" {
			k = []byte{}
		}
		if _, ok := n.Get(k); !ok {
			cr := &CacheRecord{
				Type: CacheRecordFile,
			}
			if fi.Mode()&os.ModeSymlink != 0 {
				cr.Type = CacheRecordSymlink
				link, err := os.Readlink(path)
				if err != nil {
					return err
				}
				cr.Link = filepath.ToSlash(link)
			}
			if fi.IsDir() {
				cr.Type = CacheRecordDirHeader
				cr2 := &CacheRecord{
					Type: CacheRecordDir,
				}
				txn.Insert(k, cr2)
				k = append(k, []byte("/")...)
			}
			txn.Insert(k, cr)
		}
		return nil
	})
	if err != nil {
		return err
	}

	cc.tree = txn.Commit()
	return nil
}

func prepareDigest(fp, p string, fi os.FileInfo) (digest.Digest, error) {
	h, err := NewFileHash(fp, fi)
	if err != nil {
		return "", errors.Wrapf(err, "failed to create hash for %s", p)
	}
	if fi.Mode().IsRegular() && fi.Size() > 0 {
		// TODO: would be nice to put the contents to separate hash first
		// so it can be cached for hardlinks
		f, err := os.Open(fp)
		if err != nil {
			return "", errors.Wrapf(err, "failed to open %s", p)
		}
		defer f.Close()
		if _, err := pools.Copy(h, f); err != nil {
			return "", errors.Wrapf(err, "failed to copy file data for %s", p)
		}
	}
	return digest.NewDigest(digest.SHA256, h), nil
}

func addParentToMap(d string, m map[string]struct{}) {
	if d == "" {
		return
	}
	d = path.Dir(d)
	if d == "/" {
		d = ""
	}
	m[d] = struct{}{}
	addParentToMap(d, m)
}
