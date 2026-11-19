package mounts

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/containerd/containerd/v2/core/mount"
	"github.com/containerd/continuity/fs"
	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/snapshot"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/bklog"
	"github.com/moby/sys/user"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
)

const (
	hostCacheTmpPrefix     = ".tmp-"
	hostCacheTrashPrefix   = ".trash-"
	hostCacheBasePrefix    = "base-"
	hostCacheLockSuffix    = ".lock"
	hostCacheRetiredSuffix = ".retired"
	hostCacheMarkerLock    = ".retired.lock"
	hostCacheCreateLock    = ".create.lock"

	hostCacheLockPollInterval = 100 * time.Millisecond
	hostCacheRetryInterval    = 10 * time.Millisecond
	// The marker lock is held for a few filesystem calls only.
	hostCacheMarkerPollInterval = time.Millisecond
)

// hostCacheCleanupWait bounds how long cleanup that no build waits for, such as
// removing a retired instance on release, waits for a marker lock.
var hostCacheCleanupWait = 5 * time.Second

// HostCacheMounts backs cache mounts with selected IDs by directories on the
// daemon host instead of snapshots, so their contents outlive the BuildKit
// state directory and can be shared by several daemons.
//
// The table is only ever built from daemon configuration: a build selects a
// directory by cache mount ID but can never name a host path.
//
// Each configured directory holds numbered instances, each guarded by an
// advisory flock(2) lock file next to it:
//
//	<path>/0/            cache contents mounted into the build
//	<path>/0.lock
//	<path>/0.retired     present once a reset was requested while 0 was in use
//	<path>/0.retired.lock  orders writing 0.retired with moving 0 away
//	<path>/1/            created only when instance 0 is busy (see acquire)
//	<path>/.create.lock  serializes creating and recreating instances
//	<path>/base-<key>/   the same layout, for mounts that start from a base
//
// An instance is moved away only while holding an exclusive lock on its own
// lock file, and created or recreated only while also holding the create lock.
// Lock files are never removed, so daemons sharing <path> over a filesystem
// with working flock(2) coordinate through these files alone.
type HostCacheMounts struct {
	roots map[string]*hostCacheDir
}

// NewHostCacheMounts validates an ID -> host directory table and creates the
// directories that do not exist yet.
func NewHostCacheMounts(paths map[string]string) (*HostCacheMounts, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	if err := hostCacheMountsSupported(); err != nil {
		return nil, err
	}
	h := &HostCacheMounts{roots: make(map[string]*hostCacheDir, len(paths))}
	owners := map[string]string{}
	for rawID, p := range paths {
		id := normalizeHostCacheID(rawID)
		if id == "" {
			return nil, errors.Errorf("cache mount %q: invalid empty id", rawID)
		}
		if _, ok := h.roots[id]; ok {
			return nil, errors.Errorf("cache mount %q is configured more than once", rawID)
		}
		if !filepath.IsAbs(p) {
			return nil, errors.Errorf("cache mount %q: path %q must be absolute", rawID, p)
		}
		resolved, err := prepareHostCacheRoot(p)
		if err != nil {
			return nil, errors.Wrapf(err, "cache mount %q", rawID)
		}
		for other, otherID := range owners {
			if isSubpath(other, resolved) || isSubpath(resolved, other) {
				return nil, errors.Errorf("cache mounts %q and %q use overlapping paths %q and %q", otherID, rawID, other, resolved)
			}
		}
		owners[resolved] = rawID
		h.roots[id] = &hostCacheDir{id: id, path: resolved}
	}
	for _, d := range h.roots {
		d.cleanupLeftovers()
	}
	return h, nil
}

// prepareHostCacheRoot creates the configured directory and checks that only
// the daemon's user can reach it: build output lands there as-is, setuid
// binaries and device nodes included, and whoever owns the directory can swap
// the instances builds mount.
func prepareHostCacheRoot(p string) (string, error) {
	if err := os.MkdirAll(p, 0o700); err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", err
	}
	fi, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !fi.IsDir() {
		return "", errors.Errorf("%s is not a directory", p)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return "", errors.Errorf("%s must not be accessible by group or others (mode %04o); restrict it with chmod 0700", p, fi.Mode().Perm())
	}
	uid, _, _, err := fileOwnerAndMode(fi)
	if err != nil {
		return "", err
	}
	if uid != os.Geteuid() {
		return "", errors.Errorf("%s must be owned by the user buildkitd runs as (uid %d), not uid %d", p, os.Geteuid(), uid)
	}
	return resolved, nil
}

// normalizeHostCacheID compares IDs as clean paths without leading slashes,
// because the Dockerfile frontend joins BUILDKIT_CACHE_MOUNT_NS, "/" and the
// mount's id or absolute target: "id=gocache" arrives as "/gocache",
// "target=/root/.cache" as "//root/.cache", and with a namespace as
// "ns//root/.cache".
func normalizeHostCacheID(id string) string {
	return strings.TrimPrefix(path.Clean("/"+id), "/")
}

func isSubpath(parent, p string) bool {
	rel, err := filepath.Rel(parent, p)
	return err == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))))
}

func (h *HostCacheMounts) lookup(id string) *hostCacheDir {
	if h == nil {
		return nil
	}
	return h.roots[normalizeHostCacheID(id)]
}

// Prune resets the host directories behind the given cache mount IDs, as
// requested by builds that ignore the cache. Instances in use by another build
// keep their contents for that build and are retired, so every later build,
// including the one that asked for the reset, starts from an empty instance.
func (h *HostCacheMounts) Prune(ctx context.Context, ids map[string]bool) error {
	for id := range ids {
		d := h.lookup(id)
		if d == nil {
			continue
		}
		dirs, err := d.withBases()
		if err != nil {
			return errors.Wrapf(err, "failed to reset host cache mount %q", id)
		}
		for _, dd := range dirs {
			if err := dd.prune(ctx); err != nil {
				return errors.Wrapf(err, "failed to reset host cache mount %q", id)
			}
		}
	}
	return nil
}

// hostCacheBase describes the directory a cache mount starts from when it has
// an input, e.g. the uid/gid/mode-carrying directory of a Dockerfile cache
// mount. Mounts with different bases get separate instances, as snapshot cache
// mounts do.
type hostCacheBase struct {
	selector string
	uid, gid int
	mode     uint32
}

func (b *hostCacheBase) key() string {
	return fmt.Sprintf("%s%d-%d-%04o-%s", hostCacheBasePrefix, b.uid, b.gid, b.mode, digest.FromString(b.selector).Encoded()[:12])
}

type hostCacheDir struct {
	id   string
	path string
}

func (d *hostCacheDir) forBase(b *hostCacheBase) (*hostCacheDir, error) {
	if b == nil {
		return d, nil
	}
	p := filepath.Join(d.path, b.key())
	if err := os.Mkdir(p, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	if err := requireDir(p); err != nil {
		return nil, err
	}
	return &hostCacheDir{id: d.id, path: p}, nil
}

func (d *hostCacheDir) withBases() ([]*hostCacheDir, error) {
	entries, err := os.ReadDir(d.path)
	if err != nil {
		return nil, err
	}
	dirs := []*hostCacheDir{d}
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), hostCacheBasePrefix) {
			dirs = append(dirs, &hostCacheDir{id: d.id, path: filepath.Join(d.path, e.Name())})
		}
	}
	return dirs, nil
}

type hostCacheLease struct {
	d    *hostCacheDir
	n    int
	dir  string
	lock *os.File
}

// release drops the lease. When the instance is retired, the last holder to
// release removes it rather than leaving discarded data until a later reset.
func (l *hostCacheLease) release() error {
	err := unlockFile(l.lock)
	// Unlock before trying the exclusive lock: where a failed upgrade keeps the
	// shared lock (macOS, NFS), holders releasing together would otherwise each
	// block the other and none would remove the instance.
	if retired, _ := pathExists(l.dir + hostCacheRetiredSuffix); retired && err == nil {
		if locked, lerr := tryLockFile(l.lock, true); lerr == nil && locked {
			ctx, cancel := context.WithTimeoutCause(context.Background(), hostCacheCleanupWait, errors.WithStack(context.DeadlineExceeded))
			rerr := l.d.removeRetired(ctx, l.n)
			cancel()
			if rerr != nil {
				bklog.L.WithError(rerr).Warnf("failed to remove retired host cache mount instance %s", l.dir)
			}
			err = unlockFile(l.lock)
		}
	}
	if cerr := l.lock.Close(); err == nil {
		err = cerr
	}
	return err
}

func (d *hostCacheDir) instancePath(n int) string {
	return filepath.Join(d.path, strconv.Itoa(n))
}

func (d *hostCacheDir) instances() ([]int, error) {
	entries, err := os.ReadDir(d.path)
	if err != nil {
		return nil, err
	}
	var out []int
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		n, err := strconv.Atoi(e.Name())
		if err != nil || n < 0 || strconv.Itoa(n) != e.Name() {
			continue
		}
		out = append(out, n)
	}
	slices.Sort(out)
	return out, nil
}

// ready reports whether instance n exists and has not been retired. Anything
// other than a real directory at an instance path means the directory was
// tampered with, and is refused rather than mounted into a build.
func (d *hostCacheDir) ready(n int) (bool, error) {
	exists, err := instanceExists(d.instancePath(n))
	if err != nil || !exists {
		return false, err
	}
	retired, err := pathExists(d.instancePath(n) + hostCacheRetiredSuffix)
	return !retired, err
}

// acquire picks an instance following the same rules as snapshot-backed cache
// mounts:
//   - SHARED takes a shared lock, so any number of builds on any daemon use the
//     instance together; it only moves to another instance while a LOCKED or
//     PRIVATE build holds the first one.
//   - LOCKED takes an exclusive lock and waits until an instance is free.
//   - PRIVATE takes an exclusive lock and creates a new instance instead of
//     waiting.
func (d *hostCacheDir) acquire(ctx context.Context, sharing pb.CacheSharingOpt, init func(dir string) error) (*hostCacheLease, error) {
	exclusive := false
	switch sharing {
	case pb.CacheSharingOpt_SHARED:
	case pb.CacheSharingOpt_LOCKED, pb.CacheSharingOpt_PRIVATE:
		exclusive = true
	default:
		return nil, errors.Errorf("invalid cache sharing option: %s", sharing.String())
	}
	for {
		l, anyReady, err := d.tryReady(exclusive)
		if err != nil || l != nil {
			return l, err
		}
		if sharing != pb.CacheSharingOpt_LOCKED || !anyReady {
			if l, err = d.tryCreate(ctx, sharing, init); err != nil || l != nil {
				return l, err
			}
		}
		wait := hostCacheRetryInterval
		if sharing == pb.CacheSharingOpt_LOCKED && anyReady {
			wait = hostCacheLockPollInterval
		}
		select {
		case <-ctx.Done():
			return nil, context.Cause(ctx)
		case <-time.After(wait):
		}
	}
}

// tryReady locks the first ready instance that is free, without blocking.
func (d *hostCacheDir) tryReady(exclusive bool) (_ *hostCacheLease, anyReady bool, _ error) {
	existing, err := d.instances()
	if err != nil {
		return nil, false, err
	}
	for _, n := range existing {
		ok, err := d.ready(n)
		if err != nil {
			return nil, false, err
		}
		if !ok {
			continue
		}
		anyReady = true
		l, err := d.tryLockInstance(n, exclusive)
		if err != nil || l != nil {
			return l, anyReady, err
		}
	}
	return nil, anyReady, nil
}

func (d *hostCacheDir) tryLockInstance(n int, exclusive bool) (*hostCacheLease, error) {
	f, err := openLockFile(d.instancePath(n) + hostCacheLockSuffix)
	if err != nil {
		return nil, err
	}
	locked, err := tryLockFile(f, exclusive)
	if err != nil || !locked {
		f.Close()
		return nil, err
	}
	// The instance may have been reset or retired between the check and the lock.
	ok, err := d.ready(n)
	if err != nil || !ok {
		unlockFile(f)
		f.Close()
		return nil, err
	}
	return &hostCacheLease{d: d, n: n, dir: d.instancePath(n), lock: f}, nil
}

// tryCreate makes a fresh instance under the create lock: it recreates the
// first retired or missing instance it can lock exclusively, or appends one.
// Holding the create lock until the new instance has its final lock mode keeps
// SHARED builds from mistaking a creator's exclusive lock for a LOCKED build
// and creating instances of their own.
func (d *hostCacheDir) tryCreate(ctx context.Context, sharing pb.CacheSharingOpt, init func(dir string) error) (*hostCacheLease, error) {
	exclusive := sharing != pb.CacheSharingOpt_SHARED
	cf, err := openLockFile(filepath.Join(d.path, hostCacheCreateLock))
	if err != nil {
		return nil, err
	}
	defer cf.Close()
	locked, err := tryLockFile(cf, true)
	if err != nil || !locked {
		return nil, err
	}
	defer unlockFile(cf)

	// A creator may have finished between our first look and taking the lock.
	// LOCKED builds then wait for that instance like any other ready one.
	l, anyReady, err := d.tryReady(exclusive)
	if err != nil || l != nil {
		return l, err
	}
	if sharing == pb.CacheSharingOpt_LOCKED && anyReady {
		return nil, nil
	}
	d.removePrefixed(hostCacheTmpPrefix)

	existing, err := d.instances()
	if err != nil {
		return nil, err
	}
	// Reuse the lowest free number, so a removed instance is replaced in place
	// and SHARED builds, which prefer low numbers, converge on it.
	next := 0
	for _, n := range existing {
		if n != next {
			break
		}
		next++
	}
	for _, n := range append(existing, next) {
		if ok, err := d.ready(n); err != nil {
			return nil, err
		} else if ok {
			continue
		}
		f, err := openLockFile(d.instancePath(n) + hostCacheLockSuffix)
		if err != nil {
			return nil, err
		}
		locked, err := tryLockFile(f, true)
		if err != nil || !locked {
			f.Close()
			if err != nil {
				return nil, err
			}
			continue
		}
		l, err := d.recreate(ctx, n, f, exclusive, init)
		if err != nil {
			unlockFile(f)
			f.Close()
			return nil, err
		}
		return l, nil
	}
	return nil, nil
}

// recreate replaces instance n, whose lock file f is held exclusively, with a
// fresh one built in a temporary directory, so no build sees it half done.
func (d *hostCacheDir) recreate(ctx context.Context, n int, f *os.File, exclusive bool, init func(dir string) error) (*hostCacheLease, error) {
	dataPath := d.instancePath(n)
	if err := d.discard(ctx, n); err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp(d.path, hostCacheTmpPrefix)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		os.RemoveAll(tmp)
		return nil, err
	}
	if init != nil {
		if err := init(tmp); err != nil {
			os.RemoveAll(tmp)
			return nil, err
		}
	}
	if err := os.Rename(tmp, dataPath); err != nil {
		os.RemoveAll(tmp)
		return nil, err
	}
	if !exclusive {
		// flock(2) converts by dropping and re-taking the lock, so a LOCKED or
		// PRIVATE build can take the new instance in between; that is the same
		// as finding it busy.
		locked, err := tryLockFile(f, false)
		if err != nil {
			return nil, err
		}
		if !locked {
			f.Close()
			return nil, nil
		}
	}
	return &hostCacheLease{d: d, n: n, dir: dataPath, lock: f}, nil
}

func (d *hostCacheDir) prune(ctx context.Context) error {
	existing, err := d.instances()
	if err != nil {
		return err
	}
	for _, n := range existing {
		if err := d.pruneInstance(ctx, n); err != nil {
			return err
		}
	}
	return nil
}

// pruneInstance moves an unused instance away, or retires one in use: its
// holders keep their files, and new builds get a fresh instance instead.
func (d *hostCacheDir) pruneInstance(ctx context.Context, n int) error {
	dataPath := d.instancePath(n)
	f, err := openLockFile(dataPath + hostCacheLockSuffix)
	if err != nil {
		return err
	}
	defer f.Close()
	locked, err := tryLockFile(f, true)
	if err != nil {
		return err
	}
	if !locked {
		bklog.G(ctx).Debugf("host cache mount %q instance %s is in use; retiring it", d.id, dataPath)
		return d.retire(ctx, n)
	}
	defer unlockFile(f)
	return d.discard(ctx, n)
}

// removeRetired discards instance n if it is retired. The caller holds the
// exclusive lock on its lock file.
func (d *hostCacheDir) removeRetired(ctx context.Context, n int) error {
	retired, err := pathExists(d.instancePath(n) + hostCacheRetiredSuffix)
	if err != nil || !retired {
		return err
	}
	return d.discard(ctx, n)
}

// discard moves instance n away together with its retired marker, so a marker
// never applies to a directory that later appears at that number, e.g. one an
// operator seeds by hand. The caller holds the exclusive lock on its lock file.
func (d *hostCacheDir) discard(ctx context.Context, n int) error {
	unlock, err := d.lockMarker(ctx, n)
	if err != nil {
		return err
	}
	defer unlock()
	dataPath := d.instancePath(n)
	exists, err := instanceExists(dataPath)
	if err != nil {
		return err
	}
	if exists {
		if err := d.moveToTrash(dataPath); err != nil {
			return err
		}
	}
	if err := os.Remove(dataPath + hostCacheRetiredSuffix); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// retire marks instance n so no new build uses it. A reset finds the instance
// busy without holding its lock, so the instance may have been moved away in
// the meantime; checking for it under the marker lock, which discard also
// holds, keeps a marker from outliving its instance and applying to a
// directory that later appears at that number.
func (d *hostCacheDir) retire(ctx context.Context, n int) error {
	unlock, err := d.lockMarker(ctx, n)
	if err != nil {
		return err
	}
	defer unlock()
	exists, err := instanceExists(d.instancePath(n))
	if err != nil || !exists {
		return err
	}
	mf, err := os.OpenFile(d.instancePath(n)+hostCacheRetiredSuffix, os.O_RDONLY|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	return mf.Close()
}

// lockMarker takes the lock that orders retiring instance n with moving it
// away. It polls like every other lock here, so a peer that stalls while
// holding it (a frozen daemon, an NFS client that lost its connection) delays
// the caller only until its context ends.
func (d *hostCacheDir) lockMarker(ctx context.Context, n int) (func(), error) {
	f, err := openLockFile(d.instancePath(n) + hostCacheMarkerLock)
	if err != nil {
		return nil, err
	}
	for {
		locked, err := tryLockFile(f, true)
		if err != nil {
			f.Close()
			return nil, err
		}
		if locked {
			return func() {
				unlockFile(f)
				f.Close()
			}, nil
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, context.Cause(ctx)
		case <-time.After(hostCacheMarkerPollInterval):
		}
	}
}

// removeFreeRetired moves away retired instances that no build holds, and
// removes markers whose instance is gone, e.g. after a crash between the two.
func (d *hostCacheDir) removeFreeRetired() {
	entries, err := os.ReadDir(d.path)
	if err != nil {
		return
	}
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), hostCacheRetiredSuffix)
		if !ok || !e.Type().IsRegular() {
			continue
		}
		n, err := strconv.Atoi(name)
		if err != nil || n < 0 || strconv.Itoa(n) != name {
			continue
		}
		f, err := openLockFile(d.instancePath(n) + hostCacheLockSuffix)
		if err != nil {
			continue
		}
		if locked, _ := tryLockFile(f, true); locked {
			ctx, cancel := context.WithTimeoutCause(context.Background(), hostCacheCleanupWait, errors.WithStack(context.DeadlineExceeded))
			if err := d.removeRetired(ctx, n); err != nil {
				bklog.L.WithError(err).Warnf("failed to remove retired host cache mount instance %s", d.instancePath(n))
			}
			cancel()
			unlockFile(f)
		}
		f.Close()
	}
}

// moveToTrash renames p aside and deletes it in the background. Nothing refers
// to a trash directory once renamed.
func (d *hostCacheDir) moveToTrash(p string) error {
	trash := filepath.Join(d.path, hostCacheTrashPrefix+identity.NewID())
	if err := os.Rename(p, trash); err != nil {
		return err
	}
	go removeAll(trash)
	return nil
}

// cleanupLeftovers removes what an interrupted reset or creation left behind,
// e.g. after a daemon restart, and retired instances whose builds are gone.
// Temporary directories are only removed when no daemon is creating an instance.
func (d *hostCacheDir) cleanupLeftovers() {
	dirs, err := d.withBases()
	if err != nil {
		return
	}
	for _, dd := range dirs {
		dd.removePrefixed(hostCacheTrashPrefix)
		dd.removeFreeRetired()
		cf, err := openLockFile(filepath.Join(dd.path, hostCacheCreateLock))
		if err != nil {
			continue
		}
		if locked, _ := tryLockFile(cf, true); locked {
			dd.removePrefixed(hostCacheTmpPrefix)
			unlockFile(cf)
		}
		cf.Close()
	}
}

func (d *hostCacheDir) removePrefixed(prefix string) {
	entries, err := os.ReadDir(d.path)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), prefix) {
			p := filepath.Join(d.path, e.Name())
			if prefix == hostCacheTrashPrefix {
				go removeAll(p)
			} else {
				removeAll(p)
			}
		}
	}
}

func removeAll(p string) {
	if err := os.RemoveAll(p); err != nil {
		bklog.L.WithError(err).Warnf("failed to remove host cache mount data %s", p)
	}
}

func openLockFile(p string) (*os.File, error) {
	return os.OpenFile(p, os.O_RDWR|os.O_CREATE, 0o600)
}

func pathExists(p string) (bool, error) {
	_, err := os.Lstat(p)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

// instanceExists stats p once: an instance moved away concurrently must read as
// missing, not fail the build.
func instanceExists(p string) (bool, error) {
	fi, err := os.Lstat(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if !fi.IsDir() {
		return false, errors.Errorf("refusing to use %s: not a directory", p)
	}
	return true, nil
}

func requireDir(p string) error {
	fi, err := os.Lstat(p)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return errors.Errorf("refusing to use %s: not a directory", p)
	}
	return nil
}

// hostCacheShare is one acquired instance, shared by every mount of the same
// cache ID and base within a single op: an op that mounts a LOCKED ID twice
// must not wait for itself.
type hostCacheShare struct {
	key   string
	lease *hostCacheLease
	refs  int
	idmap *user.IdentityMapping
}

func (mm *MountManager) getHostCacheDir(ctx context.Context, d *hostCacheDir, ref cache.ImmutableRef, selector string, sharing pb.CacheSharingOpt, g session.Group) (cache.MutableRef, error) {
	mm.cacheMountsMu.Lock()
	defer mm.cacheMountsMu.Unlock()

	var base *hostCacheBase
	if ref != nil {
		var err error
		if base, err = readHostCacheBase(ctx, d.id, ref, selector, g); err != nil {
			return nil, err
		}
	}
	shareKey := d.id
	if base != nil {
		shareKey += "\x00" + base.key()
	}
	if s, ok := mm.hostShares[shareKey]; ok {
		s.refs++
		return &hostCacheRef{share: s, mm: mm}, nil
	}

	dir, err := d.forBase(base)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to prepare host cache mount %q", d.id)
	}
	idmap := mm.cm.IdentityMapping()
	lease, err := dir.acquire(ctx, sharing, func(instance string) error {
		return initHostCacheInstance(instance, base, idmap)
	})
	if err != nil {
		return nil, errors.Wrapf(err, "failed to acquire host cache mount %q", d.id)
	}
	bklog.G(ctx).Debugf("using host directory %s for cache mount %q", lease.dir, d.id)

	if mm.hostShares == nil {
		mm.hostShares = map[string]*hostCacheShare{}
	}
	s := &hostCacheShare{key: shareKey, lease: lease, refs: 1, idmap: idmap}
	mm.hostShares[shareKey] = s
	return &hostCacheRef{share: s, mm: mm}, nil
}

// readHostCacheBase reads the ownership and mode of the directory a cache mount
// starts from. Only empty directories are supported: copying a populated base,
// such as a directory of an image, into a shared host directory would be both
// slow and surprising.
func readHostCacheBase(ctx context.Context, id string, ref cache.ImmutableRef, selector string, g session.Group) (*hostCacheBase, error) {
	m, err := ref.Mount(ctx, true, g)
	if err != nil {
		return nil, err
	}
	lm := snapshot.LocalMounter(m)
	root, err := lm.Mount()
	if err != nil {
		return nil, err
	}
	defer lm.Unmount()

	selector = path.Clean("/" + selector)
	p, err := fs.RootPath(root, selector)
	if err != nil {
		return nil, err
	}
	fi, err := os.Lstat(p)
	if err != nil {
		return nil, errors.Wrapf(err, "cache mount %q", id)
	}
	if !fi.IsDir() {
		return nil, errors.Errorf("cache mount %q: %s is not a directory", id, selector)
	}
	entries, err := os.ReadDir(p)
	if err != nil {
		return nil, err
	}
	if len(entries) > 0 {
		return nil, errors.Errorf("cache mount %q is backed by a host directory and can't start from the non-empty directory %s; mount it without from= and source=", id, selector)
	}
	uid, gid, mode, err := fileOwnerAndMode(fi)
	if err != nil {
		return nil, err
	}
	return &hostCacheBase{selector: selector, uid: uid, gid: gid, mode: mode}, nil
}

// initHostCacheInstance gives a new instance the same starting point as a new
// snapshot cache mount: the base's selected directory with its ownership and
// mode, or an empty root owned by the worker's root.
func initHostCacheInstance(dir string, base *hostCacheBase, idmap *user.IdentityMapping) error {
	rootUID, rootGID := -1, -1
	if idmap != nil {
		rootUID, rootGID = idmap.RootPair()
		if err := os.Lchown(dir, rootUID, rootGID); err != nil {
			return err
		}
	}
	if base == nil || base.selector == "/" {
		if base != nil {
			return applyOwnerAndMode(dir, base.uid, base.gid, base.mode)
		}
		return nil
	}
	p := dir
	parts := strings.Split(strings.TrimPrefix(base.selector, "/"), "/")
	for i, part := range parts {
		p = filepath.Join(p, part)
		if err := os.Mkdir(p, 0o755); err != nil {
			return err
		}
		if i < len(parts)-1 {
			if idmap != nil {
				if err := os.Lchown(p, rootUID, rootGID); err != nil {
					return err
				}
			}
			continue
		}
		if err := applyOwnerAndMode(p, base.uid, base.gid, base.mode); err != nil {
			return err
		}
	}
	return nil
}

// hostCacheRef lets a host directory flow through PrepareMounts as a cache
// mount. Cache mounts are never committed, so only Mount, Release and the
// identity accessors are reached; the remaining metadata methods are not
// backed by any record and must not be called.
type hostCacheRef struct {
	cache.RefMetadata
	share    *hostCacheShare
	mm       *MountManager
	released bool
}

var _ cache.MutableRef = &hostCacheRef{}

func (r *hostCacheRef) ID() string { return "hostcache:" + r.share.lease.dir }

func (r *hostCacheRef) GetDescription() string {
	return "host cache mount " + r.share.lease.dir
}

func (r *hostCacheRef) GetRecordType() client.UsageRecordType {
	return client.UsageRecordTypeCacheMount
}

func (r *hostCacheRef) IdentityMapping() *user.IdentityMapping { return r.share.idmap }

func (r *hostCacheRef) DescHandler(digest.Digest) *cache.DescHandler { return nil }

func (r *hostCacheRef) Commit(context.Context) (cache.ImmutableRef, error) {
	return nil, errors.New("host cache mounts cannot be committed")
}

func (r *hostCacheRef) Release(ctx context.Context) error {
	r.mm.cacheMountsMu.Lock()
	defer r.mm.cacheMountsMu.Unlock()
	if r.released {
		return nil
	}
	r.released = true
	r.share.refs--
	if r.share.refs > 0 {
		return nil
	}
	delete(r.mm.hostShares, r.share.key)
	return r.share.lease.release()
}

func (r *hostCacheRef) Mount(ctx context.Context, readonly bool, g session.Group) (snapshot.Mountable, error) {
	return &hostCacheMountable{path: r.share.lease.dir, readonly: readonly, idmap: r.share.idmap}, nil
}

type hostCacheMountable struct {
	path     string
	readonly bool
	idmap    *user.IdentityMapping
}

func (m *hostCacheMountable) Mount() ([]mount.Mount, func() error, error) {
	opts := []string{"rbind"}
	if m.readonly {
		opts = append(opts, "ro")
	}
	return []mount.Mount{{Type: "bind", Source: m.path, Options: opts}}, func() error { return nil }, nil
}

func (m *hostCacheMountable) IdentityMapping() *user.IdentityMapping { return m.idmap }
