package contenthash

import (
	"bytes"
	"context"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	iradix "github.com/hashicorp/go-immutable-radix/v2"
	simplelru "github.com/hashicorp/golang-lru/v2/simplelru"
	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/snapshot"
	"github.com/moby/buildkit/util/cachedigest"
	"github.com/moby/locker"
	"github.com/moby/patternmatcher"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
	"github.com/tonistiigi/fsutil"
	fstypes "github.com/tonistiigi/fsutil/types"
)

var errNotFound = errors.Errorf("not found")

var (
	defaultManager     *cacheManager
	defaultManagerOnce sync.Once
)

func getDefaultManager() *cacheManager {
	defaultManagerOnce.Do(func() {
		lru, _ := simplelru.NewLRU[string, *cacheContext](20, nil) // error is impossible on positive size
		defaultManager = &cacheManager{lru: lru, locker: locker.New()}
	})
	return defaultManager
}

const (
	CacheRecordTypeFile      = CacheRecordType_FILE
	CacheRecordTypeDir       = CacheRecordType_DIR
	CacheRecordTypeDirHeader = CacheRecordType_DIR_HEADER
	CacheRecordTypeSymlink   = CacheRecordType_SYMLINK
)

// Layout in the radix tree: Every path is saved by cleaned absolute unix path.
// Directories have 2 records, one contains digest for directory header, other
// the recursive digest for directory contents. "/dir/" is the record for
// header, "/dir" is for contents. For the root node "" (empty string) is the
// key for root, "/" for the root header

type ChecksumOpts struct {
	FollowLinks     bool
	Wildcard        bool
	IncludePatterns []string
	ExcludePatterns []string
}

func Checksum(ctx context.Context, ref cache.ImmutableRef, path string, opts ChecksumOpts, s session.Group) (digest.Digest, error) {
	return getDefaultManager().Checksum(ctx, ref, path, opts, s)
}

func GetCacheContext(ctx context.Context, md cache.RefMetadata) (CacheContext, error) {
	return getDefaultManager().GetCacheContext(ctx, md)
}

func SetCacheContext(ctx context.Context, md cache.RefMetadata, cc CacheContext) error {
	return getDefaultManager().SetCacheContext(ctx, md, cc)
}

func ClearCacheContext(md cache.RefMetadata) {
	getDefaultManager().clearCacheContext(md.ID())
}

type CacheContext interface {
	Checksum(ctx context.Context, ref cache.Mountable, p string, opts ChecksumOpts, s session.Group) (digest.Digest, error)
	HandleChange(kind fsutil.ChangeKind, p string, fi os.FileInfo, err error) error
}

type Hashed interface {
	Digest() digest.Digest
}

type includedPath struct {
	path             string
	record           *CacheRecord
	included         bool
	includeMatchInfo patternmatcher.MatchInfo
	excludeMatchInfo patternmatcher.MatchInfo
}

type cacheManager struct {
	locker *locker.Locker
	lru    *simplelru.LRU[string, *cacheContext]
	lruMu  sync.Mutex
}

func (cm *cacheManager) Checksum(ctx context.Context, ref cache.ImmutableRef, p string, opts ChecksumOpts, s session.Group) (digest.Digest, error) {
	if ref == nil {
		if p == "/" {
			return digest.FromBytes(nil), nil
		}
		return "", errors.Errorf("%s: no such file or directory", p)
	}
	cc, err := cm.GetCacheContext(ctx, ensureOriginMetadata(ref))
	if err != nil {
		return "", nil
	}
	// only applies for Windows, it's a no-op on non-Windows.
	enableProcessPrivileges()
	defer disableProcessPrivileges()
	return cc.Checksum(ctx, ref, p, opts, s)
}

func (cm *cacheManager) GetCacheContext(ctx context.Context, md cache.RefMetadata) (CacheContext, error) {
	cm.locker.Lock(md.ID())
	cm.lruMu.Lock()
	v, ok := cm.lru.Get(md.ID())
	cm.lruMu.Unlock()
	if ok {
		cm.locker.Unlock(md.ID())
		v.mu.Lock() // locking is required because multiple ImmutableRefs can reach this code; however none of them use the linkMap.
		v.linkMap = map[string][][]byte{}
		v.mu.Unlock()
		return v, nil
	}
	cc, err := newCacheContext(md)
	if err != nil {
		cm.locker.Unlock(md.ID())
		return nil, err
	}
	cm.lruMu.Lock()
	cm.lru.Add(md.ID(), cc)
	cm.lruMu.Unlock()
	cm.locker.Unlock(md.ID())
	return cc, nil
}

func (cm *cacheManager) SetCacheContext(ctx context.Context, md cache.RefMetadata, cci CacheContext) error {
	cc, ok := cci.(*cacheContext)
	if !ok {
		return errors.Errorf("invalid cachecontext: %T", cc)
	}
	if md.ID() != cc.md.ID() {
		cc = &cacheContext{
			md:       cacheMetadata{md},
			tree:     cci.(*cacheContext).tree,
			dirtyMap: map[string]struct{}{},
			linkMap:  map[string][][]byte{},
		}
	} else {
		if err := cc.save(); err != nil {
			return err
		}
	}
	cm.lruMu.Lock()
	cm.lru.Add(md.ID(), cc)
	cm.lruMu.Unlock()
	return nil
}

func (cm *cacheManager) clearCacheContext(id string) {
	cm.lruMu.Lock()
	cm.lru.Remove(id)
	cm.lruMu.Unlock()
}

type cacheContext struct {
	mu    sync.RWMutex
	md    cacheMetadata
	tree  *iradix.Tree[*CacheRecord]
	dirty bool // needs to be persisted to disk

	// used in HandleChange
	txn      *iradix.Txn[*CacheRecord]
	node     *iradix.Node[*CacheRecord]
	dirtyMap map[string]struct{}
	linkMap  map[string][][]byte
}

type cacheMetadata struct {
	cache.RefMetadata
}

const keyContentHash = "buildkit.contenthash.v0"

func (md cacheMetadata) GetContentHash() ([]byte, error) {
	return md.GetExternal(keyContentHash)
}

func (md cacheMetadata) SetContentHash(dt []byte) error {
	return md.SetExternal(keyContentHash, dt)
}

type mount struct {
	mountable cache.Mountable
	mountPath string
	unmount   func() error
	session   session.Group
}

func (m *mount) mount(ctx context.Context) (string, error) {
	if m.mountPath != "" {
		return m.mountPath, nil
	}
	mounts, err := m.mountable.Mount(ctx, true, m.session)
	if err != nil {
		return "", err
	}

	lm := snapshot.LocalMounter(mounts)

	mp, err := lm.Mount()
	if err != nil {
		return "", err
	}

	m.mountPath = mp
	m.unmount = lm.Unmount
	return mp, nil
}

func (m *mount) clean() error {
	if m.mountPath != "" {
		if err := m.unmount(); err != nil {
			return err
		}
		m.mountPath = ""
	}
	return nil
}

func newCacheContext(md cache.RefMetadata) (*cacheContext, error) {
	cc := &cacheContext{
		md:       cacheMetadata{md},
		tree:     iradix.New[*CacheRecord](),
		dirtyMap: map[string]struct{}{},
		linkMap:  map[string][][]byte{},
	}
	if err := cc.load(); err != nil {
		return nil, err
	}
	return cc, nil
}

func (cc *cacheContext) load() error {
	dt, err := cc.md.GetContentHash()
	if err != nil {
		return nil
	}

	var l CacheRecords
	if err := l.UnmarshalVT(dt); err != nil {
		return err
	}

	txn := cc.tree.Txn()
	for _, p := range l.Paths {
		txn.Insert([]byte(p.Path), p.Record)
	}
	cc.tree = txn.Commit()
	return nil
}

func (cc *cacheContext) save() error {
	cc.mu.Lock()
	defer cc.mu.Unlock()

	if cc.txn != nil {
		cc.commitActiveTransaction()
	}

	var l CacheRecords
	node := cc.tree.Root()
	node.Walk(func(k []byte, v *CacheRecord) bool {
		l.Paths = append(l.Paths, &CacheRecordWithPath{
			Path:   string(k),
			Record: v,
		})
		return false
	})

	dt, err := l.MarshalVT()
	if err != nil {
		return err
	}

	return cc.md.SetContentHash(dt)
}

func keyPath(p string) string {
	p = path.Join("/", filepath.ToSlash(p))
	if p == "/" {
		p = ""
	}
	return p
}

// HandleChange notifies the source about a modification operation
func (cc *cacheContext) HandleChange(kind fsutil.ChangeKind, p string, fi os.FileInfo, err error) (retErr error) {
	p = keyPath(p)
	k := convertPathToKey(p)

	deleteDir := func(cr *CacheRecord) {
		if cr.Type == CacheRecordTypeDir {
			cc.node.WalkPrefix(append(k, 0), func(k []byte, v *CacheRecord) bool {
				cc.txn.Delete(k)
				return false
			})
		}
	}

	cc.mu.Lock()
	defer cc.mu.Unlock()
	if cc.txn == nil {
		cc.txn = cc.tree.Txn()
		cc.node = cc.tree.Root()

		// root is not called by HandleChange. need to fake it
		if _, ok := cc.node.Get([]byte{0}); !ok {
			cc.txn.Insert([]byte{0}, &CacheRecord{
				Type:   CacheRecordTypeDirHeader,
				Digest: string(digest.FromBytes(nil)),
			})
			cc.txn.Insert([]byte(""), &CacheRecord{
				Type: CacheRecordTypeDir,
			})
		}
	}

	if kind == fsutil.ChangeKindDelete {
		v, ok := cc.txn.Delete(k)
		if ok {
			deleteDir(v)
		}
		d := path.Dir(p)
		if d == "/" {
			d = ""
		}
		cc.dirtyMap[d] = struct{}{}
		return
	}

	stat, ok := fi.Sys().(*fstypes.Stat)
	if !ok {
		return errors.Errorf("%s invalid change without stat information", p)
	}

	h, ok := fi.(Hashed)
	if !ok {
		return errors.Errorf("invalid fileinfo: %s", p)
	}

	// if we are replacing a directory with a non-directory, rm -rf the tree under the existing dir
	v, ok := cc.node.Get(k)
	if ok {
		if v.Type == CacheRecordTypeDir && !fi.IsDir() {
			deleteDir(v)
		}
	}

	cr := &CacheRecord{
		Type: CacheRecordTypeFile,
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		cr.Type = CacheRecordTypeSymlink
		cr.Linkname = filepath.ToSlash(stat.Linkname)
	}
	if fi.IsDir() {
		cr.Type = CacheRecordTypeDirHeader
		cr2 := &CacheRecord{
			Type: CacheRecordTypeDir,
		}
		cc.txn.Insert(k, cr2)
		k = append(k, 0)
		p += "/"
	}
	cr.Digest = string(h.Digest())

	// if we receive a hardlink just use the digest of the source
	// note that the source may be called later because data writing is async
	if fi.Mode()&os.ModeSymlink == 0 && stat.Linkname != "" {
		ln := path.Join("/", filepath.ToSlash(stat.Linkname))
		v, ok := cc.txn.Get(convertPathToKey(ln))
		if ok {
			cr = v.CloneVT()
		}
		cc.linkMap[ln] = append(cc.linkMap[ln], k)
	}

	cc.txn.Insert(k, cr)
	if !fi.IsDir() {
		if links, ok := cc.linkMap[p]; ok {
			for _, l := range links {
				pp := convertKeyToPath(l)
				cc.txn.Insert(l, cr)
				d := path.Dir(pp)
				if d == "/" {
					d = ""
				}
				cc.dirtyMap[d] = struct{}{}
			}
			delete(cc.linkMap, p)
		}
	}

	d := path.Dir(p)
	if d == "/" {
		d = ""
	}
	cc.dirtyMap[d] = struct{}{}

	return nil
}

func (cc *cacheContext) Checksum(ctx context.Context, mountable cache.Mountable, p string, opts ChecksumOpts, s session.Group) (digest.Digest, error) {
	m := &mount{mountable: mountable, session: s}
	defer m.clean()

	if !opts.Wildcard && len(opts.IncludePatterns) == 0 && len(opts.ExcludePatterns) == 0 {
		return cc.lazyChecksum(ctx, m, p, opts.FollowLinks)
	}

	includedPaths, err := cc.includedPaths(ctx, m, p, opts)
	if err != nil {
		return "", err
	}

	if opts.FollowLinks {
		for i, w := range includedPaths {
			if w.record.Type == CacheRecordTypeSymlink {
				dgst, err := cc.lazyChecksum(ctx, m, w.path, opts.FollowLinks)
				if err != nil {
					return "", err
				}
				includedPaths[i].record = &CacheRecord{Digest: string(dgst)}
			}
		}
	}
	if len(includedPaths) == 0 {
		return digest.FromBytes([]byte{}), nil
	}

	if len(includedPaths) == 1 && path.Base(p) == path.Base(includedPaths[0].path) {
		return digest.Digest(includedPaths[0].record.Digest), nil
	}

	h := cachedigest.NewHash(cachedigest.TypeFileList)
	for i, w := range includedPaths {
		if i != 0 {
			h.Write([]byte{0})
		}
		h.Write([]byte(path.Base(w.path)))
		h.Write([]byte(w.record.Digest))
	}
	return h.Sum(), nil
}

func (cc *cacheContext) includedPaths(ctx context.Context, m *mount, p string, opts ChecksumOpts) ([]*includedPath, error) {
	cc.mu.Lock()
	defer cc.mu.Unlock()

	if cc.txn != nil {
		cc.commitActiveTransaction()
	}

	root := cc.tree.Root()
	scan, err := cc.needsScan(root, "", false)
	if err != nil {
		return nil, err
	}
	if scan {
		if err := cc.scanPath(ctx, m, "", false); err != nil {
			return nil, err
		}
	}

	defer func() {
		if cc.dirty {
			go cc.save()
			cc.dirty = false
		}
	}()

	endsInSep := len(p) != 0 && p[len(p)-1] == filepath.Separator
	p = keyPath(p)

	var includePatternMatcher *patternmatcher.PatternMatcher
	if len(opts.IncludePatterns) != 0 {
		includePatternMatcher, err = patternmatcher.New(opts.IncludePatterns)
		if err != nil {
			return nil, errors.Wrapf(err, "invalid includepatterns: %s", opts.IncludePatterns)
		}
	}

	var excludePatternMatcher *patternmatcher.PatternMatcher
	if len(opts.ExcludePatterns) != 0 {
		excludePatternMatcher, err = patternmatcher.New(opts.ExcludePatterns)
		if err != nil {
			return nil, errors.Wrapf(err, "invalid excludepatterns: %s", opts.ExcludePatterns)
		}
	}

	includedPaths := make([]*includedPath, 0, 2)

	txn := cc.tree.Txn()
	root = txn.Root()
	var (
		updated        bool
		iter           *iradix.Iterator[*CacheRecord]
		k              []byte
		keyOk          bool
		origPrefix     string
		resolvedPrefix string
	)

	iter = root.Iterator()

	if opts.Wildcard {
		origPrefix, k, keyOk, err = wildcardPrefix(root, p)
		if err != nil {
			return nil, err
		}
	} else {
		origPrefix = p
		k = convertPathToKey(origPrefix)

		// We need to resolve symlinks here, in case the base path
		// involves a symlink. That will match fsutil behavior of
		// calling functions such as stat and walk.
		var cr *CacheRecord
		k, cr, err = getFollowLinks(root, k, false)
		if err != nil {
			return nil, err
		}
		keyOk = (cr != nil)
	}

	if origPrefix != "" {
		if keyOk {
			iter.SeekLowerBound(append(slices.Clone(k), 0))
		}

		resolvedPrefix = convertKeyToPath(k)
	} else {
		k, _, keyOk = iter.Next()
	}

	var (
		parentDirHeaders []*includedPath
		lastMatchedDir   string
	)

	for keyOk {
		fn := convertKeyToPath(k)

		// Convert the path prefix from what we found in the prefix
		// tree to what the argument specified.
		//
		// For example, if the original 'p' argument was /a/b and there
		// is a symlink a->c, we want fn to be /a/b/foo rather than
		// /c/b/foo. This is necessary to ensure correct pattern
		// matching.
		//
		// When wildcards are enabled, this translation applies to the
		// portion of 'p' before any wildcards.
		if after, ok := strings.CutPrefix(fn, resolvedPrefix); ok {
			fn = origPrefix + after
		}

		for len(parentDirHeaders) != 0 {
			lastParentDir := parentDirHeaders[len(parentDirHeaders)-1]
			if strings.HasPrefix(fn, lastParentDir.path+"/") {
				break
			}
			parentDirHeaders = parentDirHeaders[:len(parentDirHeaders)-1]
		}
		var parentDir *includedPath
		if len(parentDirHeaders) != 0 {
			parentDir = parentDirHeaders[len(parentDirHeaders)-1]
		}

		dirHeader := false
		if len(k) > 0 && k[len(k)-1] == byte(0) {
			dirHeader = true
			fn = fn[:len(fn)-1]
			if fn == p && endsInSep {
				// We don't include the metadata header for a source dir which ends with a separator
				k, _, keyOk = iter.Next()
				continue
			}
		}

		maybeIncludedPath := &includedPath{path: fn}
		var shouldInclude bool
		if opts.Wildcard {
			if p != "" && (lastMatchedDir == "" || !strings.HasPrefix(fn, lastMatchedDir+"/")) {
				include, err := path.Match(p, fn)
				if err != nil {
					return nil, err
				}
				if !include {
					k, _, keyOk = iter.Next()
					continue
				}
				lastMatchedDir = fn
			}

			shouldInclude, err = shouldIncludePath(
				strings.TrimSuffix(strings.TrimPrefix(fn+"/", lastMatchedDir+"/"), "/"),
				includePatternMatcher,
				excludePatternMatcher,
				maybeIncludedPath,
				parentDir,
			)
			if err != nil {
				return nil, err
			}
		} else {
			if !strings.HasPrefix(fn+"/", p+"/") {
				break
			}

			shouldInclude, err = shouldIncludePath(
				strings.TrimSuffix(strings.TrimPrefix(fn+"/", p+"/"), "/"),
				includePatternMatcher,
				excludePatternMatcher,
				maybeIncludedPath,
				parentDir,
			)
			if err != nil {
				return nil, err
			}
		}

		if !shouldInclude && !dirHeader {
			k, _, keyOk = iter.Next()
			continue
		}

		cr, upt, err := cc.checksum(ctx, root, txn, m, k, false)
		if err != nil {
			return nil, err
		}
		if upt {
			updated = true
		}

		if cr.Type == CacheRecordTypeDir {
			// We only hash dir headers and files, not dir contents. Hashing
			// dir contents could be wrong if there are exclusions within the
			// dir.
			shouldInclude = false
		}
		maybeIncludedPath.record = cr

		if shouldInclude {
			for _, parentDir := range parentDirHeaders {
				if !parentDir.included {
					includedPaths = append(includedPaths, parentDir)
					parentDir.included = true
				}
			}
			includedPaths = append(includedPaths, maybeIncludedPath)
			maybeIncludedPath.included = true
		}

		if cr.Type == CacheRecordTypeDirHeader {
			// We keep track of parent dir headers whether
			// they are immediately included or not, in case
			// an include pattern matches a file inside one
			// of these dirs.
			parentDirHeaders = append(parentDirHeaders, maybeIncludedPath)
		}

		k, _, keyOk = iter.Next()
	}

	cc.tree = txn.Commit()
	cc.dirty = updated

	return includedPaths, nil
}

func shouldIncludePath(
	candidate string,
	includePatternMatcher *patternmatcher.PatternMatcher,
	excludePatternMatcher *patternmatcher.PatternMatcher,
	maybeIncludedPath *includedPath,
	parentDir *includedPath,
) (bool, error) {
	var (
		m         bool
		matchInfo patternmatcher.MatchInfo
		err       error
	)
	if includePatternMatcher != nil {
		if parentDir != nil {
			m, matchInfo, err = includePatternMatcher.MatchesUsingParentResults(candidate, parentDir.includeMatchInfo)
		} else {
			m, matchInfo, err = includePatternMatcher.MatchesUsingParentResults(candidate, patternmatcher.MatchInfo{})
		}
		if err != nil {
			return false, errors.Wrap(err, "failed to match includepatterns")
		}
		maybeIncludedPath.includeMatchInfo = matchInfo
		if !m {
			return false, nil
		}
	}

	if excludePatternMatcher != nil {
		if parentDir != nil {
			m, matchInfo, err = excludePatternMatcher.MatchesUsingParentResults(candidate, parentDir.excludeMatchInfo)
		} else {
			m, matchInfo, err = excludePatternMatcher.MatchesUsingParentResults(candidate, patternmatcher.MatchInfo{})
		}
		if err != nil {
			return false, errors.Wrap(err, "failed to match excludepatterns")
		}
		maybeIncludedPath.excludeMatchInfo = matchInfo
		if m {
			return false, nil
		}
	}

	return true, nil
}

func wildcardPrefix(root *iradix.Node[*CacheRecord], p string) (string, []byte, bool, error) {
	// For consistency with what the copy implementation in fsutil
	// does: split pattern into non-wildcard prefix and rest of
	// pattern, then follow symlinks when resolving the non-wildcard
	// prefix.

	d1, d2 := splitWildcards(p)
	if d1 == "/" {
		return "", nil, false, nil
	}

	// Only resolve the final symlink component if there are components in the
	// wildcard segment.
	k, cr, err := getFollowLinks(root, convertPathToKey(d1), d2 != "")
	if err != nil {
		return "", k, false, err
	}
	return d1, k, cr != nil, nil
}

func splitWildcards(p string) (d1, d2 string) {
	parts := strings.Split(path.Join(p), "/")
	var p1, p2 []string
	var found bool
	for _, p := range parts {
		if !found && containsWildcards(p) {
			found = true
		}
		if p == "" {
			p = "/"
		}
		if !found {
			p1 = append(p1, p)
		} else {
			p2 = append(p2, p)
		}
	}
	return path.Join(p1...), path.Join(p2...)
}

func containsWildcards(name string) bool {
	for i := 0; i < len(name); i++ {
		switch name[i] {
		case '*', '?', '[':
			return true
		case '\\':
			i++
		}
	}
	return false
}

func (cc *cacheContext) lazyChecksum(ctx context.Context, m *mount, p string, followTrailing bool) (digest.Digest, error) {
	p = keyPath(p)
	k := convertPathToKey(p)

	// Try to look up the path directly without doing a scan.
	cc.mu.RLock()
	if cc.txn == nil {
		root := cc.tree.Root()
		cc.mu.RUnlock()

		_, cr, err := getFollowLinks(root, k, followTrailing)
		if err != nil {
			return "", err
		}
		if cr != nil && cr.Digest != "" {
			return digest.Digest(cr.Digest), nil
		}
	} else {
		cc.mu.RUnlock()
	}

	cc.mu.Lock()
	defer cc.mu.Unlock()

	if cc.txn != nil {
		cc.commitActiveTransaction()
	}

	defer func() {
		if cc.dirty {
			go cc.save()
			cc.dirty = false
		}
	}()

	cr, err := cc.scanChecksum(ctx, m, p, followTrailing)
	if err != nil {
		return "", err
	}
	return digest.Digest(cr.Digest), nil
}

func (cc *cacheContext) commitActiveTransaction() {
	for d := range cc.dirtyMap {
		addParentToMap(d, cc.dirtyMap)
	}
	for d := range cc.dirtyMap {
		k := convertPathToKey(d)
		if _, ok := cc.txn.Get(k); ok {
			cc.txn.Insert(k, &CacheRecord{Type: CacheRecordTypeDir})
		}
	}
	cc.tree = cc.txn.Commit()
	cc.node = nil
	cc.dirtyMap = map[string]struct{}{}
	cc.txn = nil
}

func (cc *cacheContext) scanChecksum(ctx context.Context, m *mount, p string, followTrailing bool) (*CacheRecord, error) {
	root := cc.tree.Root()
	scan, err := cc.needsScan(root, p, followTrailing)
	if err != nil {
		return nil, err
	}
	if scan {
		if err := cc.scanPath(ctx, m, p, followTrailing); err != nil {
			return nil, err
		}
	}
	k := convertPathToKey(p)
	txn := cc.tree.Txn()
	root = txn.Root()
	cr, updated, err := cc.checksum(ctx, root, txn, m, k, followTrailing)
	if err != nil {
		return nil, err
	}
	cc.tree = txn.Commit()
	cc.dirty = updated
	return cr, err
}

func (cc *cacheContext) checksum(ctx context.Context, root *iradix.Node[*CacheRecord], txn *iradix.Txn[*CacheRecord], m *mount, k []byte, followTrailing bool) (*CacheRecord, bool, error) {
	origk := k
	k, cr, err := getFollowLinks(root, k, followTrailing)
	if err != nil {
		return nil, false, err
	}
	if cr == nil {
		return nil, false, errors.Wrapf(errNotFound, "%q", convertKeyToPath(origk))
	}
	if cr.Digest != "" {
		return cr, false, nil
	}
	var dgst digest.Digest

	switch cr.Type {
	case CacheRecordTypeDir:
		h := cachedigest.NewHash(cachedigest.TypeFileList)
		next := append(k, 0)
		iter := root.Iterator()
		iter.SeekLowerBound(append(slices.Clone(next), 0))
		subk := next
		ok := true
		for ok && bytes.HasPrefix(subk, next) {
			h.Write(bytes.TrimPrefix(subk, k))

			// We do not follow trailing links when checksumming a directory's
			// contents.
			subcr, _, err := cc.checksum(ctx, root, txn, m, subk, false)
			if err != nil {
				return nil, false, err
			}

			h.Write([]byte(subcr.Digest))

			if subcr.Type == CacheRecordTypeDir { // skip subfiles
				next := append(subk, 0, 0xff)
				iter = root.Iterator()
				iter.SeekLowerBound(next)
			}
			subk, _, ok = iter.Next()
		}
		dgst = h.Sum()

	default:
		p := convertKeyToPath(bytes.TrimSuffix(k, []byte{0}))

		target, err := m.mount(ctx)
		if err != nil {
			return nil, false, err
		}

		// no FollowSymlinkInScope because invalid paths should not be inserted
		fp := filepath.Join(target, filepath.FromSlash(p))

		fi, err := os.Lstat(fp)
		if err != nil {
			return nil, false, err
		}

		dgst, err = prepareDigest(fp, p, fi)
		if err != nil {
			return nil, false, err
		}
	}

	cr2 := &CacheRecord{
		Digest:   string(dgst),
		Type:     cr.Type,
		Linkname: cr.Linkname,
	}

	txn.Insert(k, cr2)

	return cr2, true, nil
}

// pathSet is a set of path prefixes that can be used to see if a given path is
// lexically a child of any path in the set. All paths provided to this set
// MUST be absolute and use / as the separator.
type pathSet struct {
	// prefixes contains paths of the form "/a/b/", so that we correctly detect
	// /a/b as being a parent of /a/b/c but not /a/bc.
	prefixes []string
}

// add a path to the set. This is a no-op if includes(path) == true.
func (s *pathSet) add(p string) {
	// Ensure the path is absolute and clean.
	p = path.Join("/", p)
	if !s.includes(p) {
		if p != "/" {
			p += "/"
		}
		s.prefixes = append(s.prefixes, p)
	}
}

// includes returns true iff there is a path in the pathSet which is a lexical
// parent of the given path. The provided path MUST be an absolute path and
// MUST NOT contain any ".." components, as they will be path.Clean'd.
func (s pathSet) includes(p string) bool {
	// Ensure the path is absolute and clean.
	p = path.Join("/", p)
	if p != "/" {
		p += "/"
	}
	for _, prefix := range s.prefixes {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return false
}

// needsScan returns false if path is in the tree or a parent path is in tree
// and subpath is missing.
func (cc *cacheContext) needsScan(root *iradix.Node[*CacheRecord], path string, followTrailing bool) (bool, error) {
	var (
		goodPaths       pathSet
		hasParentInTree bool
	)
	k := convertPathToKey(path)
	_, cr, err := getFollowLinksCallback(root, k, followTrailing, func(subpath string, cr *CacheRecord) error {
		// If we found a path that exists in the cache, add it to the set of
		// known-scanned paths. Otherwise, verify whether the not-found subpath
		// is inside a known-scanned path (we might have hit a "..", taking us
		// out of the scanned paths, or we might hit a non-existent path inside
		// a scanned path). getFollowLinksCallback iterates left-to-right, so
		// we will always hit ancestors first.
		if cr != nil {
			hasParentInTree = cr.Type != CacheRecordTypeSymlink
			goodPaths.add(subpath)
		} else {
			hasParentInTree = goodPaths.includes(subpath)
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return cr == nil && !hasParentInTree, nil
}

// Only used by TestNeedScanChecksumRegression to make sure scanPath is not
// called for paths we have already scanned.
var (
	scanCounterEnable bool
	scanCounter       atomic.Uint64
)

func (cc *cacheContext) scanPath(ctx context.Context, m *mount, p string, followTrailing bool) (retErr error) {
	p = path.Join("/", p)

	mp, err := m.mount(ctx)
	if err != nil {
		return err
	}

	n := cc.tree.Root()
	txn := cc.tree.Txn()

	resolvedPath, err := rootPath(mp, filepath.FromSlash(p), followTrailing, func(p, link string) error {
		cr := &CacheRecord{
			Type:     CacheRecordTypeSymlink,
			Linkname: filepath.ToSlash(link),
		}
		p = path.Join("/", filepath.ToSlash(p))
		txn.Insert(convertPathToKey(p), cr)
		return nil
	})
	if err != nil {
		return err
	}

	// Scan the parent directory of the path we resolved, unless we're at the
	// root (in which case we scan the root).
	scanPath := filepath.Dir(resolvedPath)
	if !strings.HasPrefix(filepath.ToSlash(scanPath)+"/", filepath.ToSlash(mp)+"/") {
		scanPath = resolvedPath
	}

	walkFunc := func(itemPath string, fi os.FileInfo, err error) error {
		if scanCounterEnable {
			scanCounter.Add(1)
		}
		if err != nil {
			// If the root doesn't exist, ignore the error.
			if itemPath == scanPath && errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return errors.Wrapf(err, "failed to walk %s", itemPath)
		}
		rel, err := filepath.Rel(mp, itemPath)
		if err != nil {
			return err
		}
		k := convertPathToKey(keyPath(rel))
		if _, ok := n.Get(k); !ok {
			cr := &CacheRecord{
				Type: CacheRecordTypeFile,
			}
			if fi.Mode()&os.ModeSymlink != 0 {
				cr.Type = CacheRecordTypeSymlink
				link, err := os.Readlink(itemPath)
				if err != nil {
					return err
				}
				cr.Linkname = filepath.ToSlash(link)
			}
			if fi.IsDir() {
				cr.Type = CacheRecordTypeDirHeader
				cr2 := &CacheRecord{
					Type: CacheRecordTypeDir,
				}
				txn.Insert(k, cr2)
				k = append(k, 0)
			}
			txn.Insert(k, cr)
		}
		return nil
	}

	err = cc.walk(scanPath, walkFunc)

	if err != nil {
		return err
	}

	cc.tree = txn.Commit()
	return nil
}

// followLinksCallback is called after we try to resolve each element. If the
// path was not found, cr is nil.
type followLinksCallback func(path string, cr *CacheRecord) error

// getFollowLinks is shorthand for getFollowLinksCallback(..., nil).
func getFollowLinks(root *iradix.Node[*CacheRecord], k []byte, followTrailing bool) ([]byte, *CacheRecord, error) {
	return getFollowLinksCallback(root, k, followTrailing, nil)
}

// getFollowLinksCallback looks up the requested key, fully resolving any
// symlink components encountered. The implementation is heavily based on
// <https://github.com/cyphar/filepath-securejoin>.
//
// followTrailing indicates whether the *final component* of the path should be
// resolved (effectively O_PATH|O_NOFOLLOW). Note that (in contrast to some
// Linux APIs), followTrailing is obeyed even if the key has a trailing slash
// (though paths like "foo/link/." will cause the link to be resolved).
//
// cb is a callback that is called for each path component encountered during
// path resolution (after the path component is looked up in the cache). This
// means for a path like /a/b/c, the callback will be called for at least
//
//	{/, /a, /a/b, /a/b/c}
//
// Note that if any of the components are symlinks, the paths will depend on
// the symlink contents and there will be more callbacks. If the requested key
// has a trailing slash, the callback will also be called for the final
// trailing-slash lookup (/a/b/c/ in the above example). Note that
// getFollowLinksCallback will try to look up the original key directly first
// and the callback is not called for this first lookup.
func getFollowLinksCallback(root *iradix.Node[*CacheRecord], k []byte, followTrailing bool, cb followLinksCallback) ([]byte, *CacheRecord, error) {
	v, ok := root.Get(k)
	if ok && (!followTrailing || v.Type != CacheRecordTypeSymlink) {
		return k, v, nil
	}
	if len(k) == 0 {
		return k, nil, nil
	}

	var (
		currentPath   = "/"
		remainingPath = convertKeyToPath(k)
		linksWalked   int
		cr            *CacheRecord
	)
	// Trailing slashes are significant for the cache, but path.Clean strips
	// them. We only care about the slash for the final lookup.
	remainingPath, hadTrailingSlash := strings.CutSuffix(remainingPath, "/")
	for remainingPath != "" {
		// Get next component.
		var part string
		if i := strings.IndexRune(remainingPath, '/'); i == -1 {
			part, remainingPath = remainingPath, ""
		} else {
			part, remainingPath = remainingPath[:i], remainingPath[i+1:]
		}

		// Apply the component to the path. Since it is a single component, and
		// our current path contains no symlinks, we can just apply it
		// leixically.
		nextPath := keyPath(path.Join("/", currentPath, part))
		// In contrast to rootPath, we don't skip lookups for no-op components
		// or / because we need to call the callback for every path component
		// we hit (including /) and we need to make sure that the CacheRecord
		// we return is correct after every iteration.

		cr, ok = root.Get(convertPathToKey(nextPath))
		if cb != nil {
			if err := cb(nextPath, cr); err != nil {
				return nil, nil, err
			}
		}
		if !ok || cr.Type != CacheRecordTypeSymlink {
			currentPath = nextPath
			continue
		}
		if !followTrailing && remainingPath == "" {
			currentPath = nextPath
			break
		}

		linksWalked++
		if linksWalked > maxSymlinkLimit {
			return nil, nil, errTooManyLinks
		}

		remainingPath = cr.Linkname + "/" + remainingPath
		if path.IsAbs(cr.Linkname) {
			currentPath = "/"
		}
	}
	// We've already looked up the final component. However, if there was a
	// trailing slash in the original path, we need to do the lookup again with
	// the slash applied.
	if hadTrailingSlash {
		currentPath += "/"
		cr, _ = root.Get(convertPathToKey(currentPath))
		if cb != nil {
			if err := cb(currentPath, cr); err != nil {
				return nil, nil, err
			}
		}
	}
	return convertPathToKey(currentPath), cr, nil
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
		if _, err := poolsCopy(h, f); err != nil {
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

func ensureOriginMetadata(md cache.RefMetadata) cache.RefMetadata {
	em, ok := md.GetEqualMutable()
	if !ok {
		em = md
	}
	return em
}

var pool32K = sync.Pool{
	New: func() any {
		buf := make([]byte, 32*1024) // 32K
		return &buf
	},
}

func poolsCopy(dst io.Writer, src io.Reader) (written int64, err error) {
	buf := pool32K.Get().(*[]byte)
	written, err = io.CopyBuffer(dst, src, *buf)
	pool32K.Put(buf)
	return
}

func convertPathToKey(p string) []byte {
	return bytes.ReplaceAll([]byte(p), []byte("/"), []byte{0})
}

func convertKeyToPath(p []byte) string {
	return string(bytes.ReplaceAll(p, []byte{0}, []byte("/")))
}
