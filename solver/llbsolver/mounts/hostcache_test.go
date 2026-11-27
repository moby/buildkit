//go:build !windows

package mounts

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/sys/user"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
)

// privateTempDir is a temporary directory that passes the owner-only check.
func privateTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0o700))
	return dir
}

func newTestHostCacheDir(t *testing.T) *hostCacheDir {
	t.Helper()
	h, err := NewHostCacheMounts(map[string]string{"c": privateTempDir(t)})
	require.NoError(t, err)
	d := h.lookup("c")
	require.NotNil(t, d)
	return d
}

func acquireNow(t *testing.T, d *hostCacheDir, sharing pb.CacheSharingOpt) *hostCacheLease {
	t.Helper()
	ctx, cancel := context.WithTimeoutCause(t.Context(), 5*time.Second, errors.WithStack(context.DeadlineExceeded))
	defer cancel()
	l, err := d.acquire(ctx, sharing, nil)
	require.NoError(t, err)
	return l
}

func TestNewHostCacheMountsValidation(t *testing.T) {
	root := privateTempDir(t)

	h, err := NewHostCacheMounts(nil)
	require.NoError(t, err)
	require.Nil(t, h)
	require.Nil(t, h.lookup("anything"))
	require.NoError(t, h.Prune(t.Context(), map[string]bool{"anything": false}))

	_, err = NewHostCacheMounts(map[string]string{"a": "relative/path"})
	require.ErrorContains(t, err, "must be absolute")

	_, err = NewHostCacheMounts(map[string]string{"/": filepath.Join(root, "x")})
	require.ErrorContains(t, err, "invalid empty id")

	_, err = NewHostCacheMounts(map[string]string{"a": filepath.Join(root, "a"), "/a": filepath.Join(root, "b")})
	require.ErrorContains(t, err, "more than once")

	_, err = NewHostCacheMounts(map[string]string{"a": filepath.Join(root, "a"), "b": filepath.Join(root, "a", "nested")})
	require.ErrorContains(t, err, "overlapping")

	_, err = NewHostCacheMounts(map[string]string{"a": filepath.Join(root, "a"), "b": filepath.Join(root, "a") + "/"})
	require.ErrorContains(t, err, "overlapping")

	h, err = NewHostCacheMounts(map[string]string{
		"gocache":      filepath.Join(root, "go"),
		"ns/apt":       filepath.Join(root, "go-apt"),
		"/root/.cache": filepath.Join(root, "dotcache"),
	})
	require.NoError(t, err)
	require.DirExists(t, filepath.Join(root, "go"))
	// The Dockerfile frontend sends "/gocache" when no cache namespace is set.
	require.NotNil(t, h.lookup("/gocache"))
	require.NotNil(t, h.lookup("gocache"))
	require.NotNil(t, h.lookup("ns/apt"))
	// A Dockerfile cache mount without an id uses "/" + its target path.
	require.NotNil(t, h.lookup("//root/.cache"))
	require.NotNil(t, h.lookup("root/.cache"))
	// ... and with BUILDKIT_CACHE_MOUNT_NS=ns, "ns//root/.cache".
	require.NotNil(t, h.lookup("ns//apt"))
	require.Nil(t, h.lookup("other"))
}

func TestNewHostCacheMountsRefusesForeignOwner(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to create a directory owned by another user")
	}
	p := filepath.Join(privateTempDir(t), "foreign")
	require.NoError(t, os.Mkdir(p, 0o700))
	require.NoError(t, os.Chown(p, 12345, 12345))
	_, err := NewHostCacheMounts(map[string]string{"c": p})
	require.ErrorContains(t, err, "must be owned by the user buildkitd runs as")
}

func TestNewHostCacheMountsRefusesOpenPermissions(t *testing.T) {
	root := privateTempDir(t)
	p := filepath.Join(root, "open")
	require.NoError(t, os.Mkdir(p, 0o755))
	require.NoError(t, os.Chmod(p, 0o755))
	_, err := NewHostCacheMounts(map[string]string{"c": p})
	require.ErrorContains(t, err, "must not be accessible by group or others")

	require.NoError(t, os.Chmod(p, 0o700))
	_, err = NewHostCacheMounts(map[string]string{"c": p})
	require.NoError(t, err)
}

func TestNewHostCacheMountsResolvesSymlinks(t *testing.T) {
	root := privateTempDir(t)
	real := filepath.Join(root, "real")
	require.NoError(t, os.Mkdir(real, 0o700))
	require.NoError(t, os.Symlink(real, filepath.Join(root, "alias")))
	_, err := NewHostCacheMounts(map[string]string{"a": real, "b": filepath.Join(root, "alias")})
	require.ErrorContains(t, err, "overlapping")
}

func TestHostCacheSharedCoexists(t *testing.T) {
	d := newTestHostCacheDir(t)
	l1 := acquireNow(t, d, pb.CacheSharingOpt_SHARED)
	l2 := acquireNow(t, d, pb.CacheSharingOpt_SHARED)
	require.Equal(t, l1.dir, l2.dir)
	require.Equal(t, filepath.Join(d.path, "0"), l1.dir)
	require.NoError(t, l1.release())
	require.NoError(t, l2.release())
}

func TestHostCacheLockedWaits(t *testing.T) {
	d := newTestHostCacheDir(t)
	l1 := acquireNow(t, d, pb.CacheSharingOpt_LOCKED)

	ctx, cancel := context.WithTimeoutCause(t.Context(), 300*time.Millisecond, errors.WithStack(context.DeadlineExceeded))
	defer cancel()
	_, err := d.acquire(ctx, pb.CacheSharingOpt_LOCKED, nil)
	require.ErrorIs(t, err, context.DeadlineExceeded)

	got := make(chan *hostCacheLease)
	go func() {
		l, err := d.acquire(t.Context(), pb.CacheSharingOpt_LOCKED, nil)
		if err != nil {
			t.Error(err)
		}
		got <- l
	}()
	select {
	case <-got:
		t.Fatal("locked acquire did not wait for the holder")
	case <-time.After(300 * time.Millisecond):
	}
	require.NoError(t, l1.release())
	select {
	case l2 := <-got:
		require.Equal(t, l1.dir, l2.dir)
		require.NoError(t, l2.release())
	case <-time.After(5 * time.Second):
		t.Fatal("locked acquire did not proceed after release")
	}

	insts, err := d.instances()
	require.NoError(t, err)
	require.Equal(t, []int{0}, insts)
}

func TestHostCacheLockedWaitsForShared(t *testing.T) {
	d := newTestHostCacheDir(t)
	s := acquireNow(t, d, pb.CacheSharingOpt_SHARED)
	ctx, cancel := context.WithTimeoutCause(t.Context(), 300*time.Millisecond, errors.WithStack(context.DeadlineExceeded))
	defer cancel()
	_, err := d.acquire(ctx, pb.CacheSharingOpt_LOCKED, nil)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.NoError(t, s.release())
}

func TestHostCachePrivateTakesNewInstanceWhenBusy(t *testing.T) {
	d := newTestHostCacheDir(t)
	l1 := acquireNow(t, d, pb.CacheSharingOpt_PRIVATE)
	l2 := acquireNow(t, d, pb.CacheSharingOpt_PRIVATE)
	require.Equal(t, filepath.Join(d.path, "0"), l1.dir)
	require.Equal(t, filepath.Join(d.path, "1"), l2.dir)
	require.NoError(t, l1.release())

	l3 := acquireNow(t, d, pb.CacheSharingOpt_PRIVATE)
	require.Equal(t, l1.dir, l3.dir, "a free instance is reused before creating a new one")
	require.NoError(t, l2.release())
	require.NoError(t, l3.release())
}

func TestHostCacheSharedMovesAsideFromExclusiveHolder(t *testing.T) {
	d := newTestHostCacheDir(t)
	locked := acquireNow(t, d, pb.CacheSharingOpt_LOCKED)
	shared := acquireNow(t, d, pb.CacheSharingOpt_SHARED)
	require.NotEqual(t, locked.dir, shared.dir)
	require.NoError(t, locked.release())
	require.NoError(t, shared.release())

	shared = acquireNow(t, d, pb.CacheSharingOpt_SHARED)
	require.Equal(t, locked.dir, shared.dir, "shared returns to the first instance once it is free")
	require.NoError(t, shared.release())
}

func TestHostCacheInstanceCreatedOnce(t *testing.T) {
	d := newTestHostCacheDir(t)
	var inits atomic.Int32
	init := func(dir string) error {
		inits.Add(1)
		time.Sleep(50 * time.Millisecond)
		return os.WriteFile(filepath.Join(dir, "seed"), []byte("x"), 0o644)
	}

	const n = 8
	var wg sync.WaitGroup
	leases := make([]*hostCacheLease, n)
	for i := range n {
		wg.Go(func() {
			l, err := d.acquire(t.Context(), pb.CacheSharingOpt_SHARED, init)
			if err != nil {
				t.Error(err)
				return
			}
			leases[i] = l
		})
	}
	wg.Wait()
	require.EqualValues(t, 1, inits.Load())
	for _, l := range leases {
		require.NotNil(t, l)
		require.FileExists(t, filepath.Join(l.dir, "seed"))
		require.NoError(t, l.release())
	}
}

func TestHostCacheFailedInitLeavesNoInstance(t *testing.T) {
	d := newTestHostCacheDir(t)
	_, err := d.acquire(t.Context(), pb.CacheSharingOpt_SHARED, func(string) error {
		return os.ErrPermission
	})
	require.ErrorIs(t, err, os.ErrPermission)

	entries, err := os.ReadDir(d.path)
	require.NoError(t, err)
	for _, e := range entries {
		require.False(t, e.IsDir(), "unexpected directory %s", e.Name())
	}
}

func TestHostCachePrune(t *testing.T) {
	root := privateTempDir(t)
	h, err := NewHostCacheMounts(map[string]string{"c": filepath.Join(root, "c"), "other": filepath.Join(root, "other")})
	require.NoError(t, err)
	d := h.lookup("c")

	l := acquireNow(t, d, pb.CacheSharingOpt_LOCKED)
	require.NoError(t, os.WriteFile(filepath.Join(l.dir, "old"), []byte("x"), 0o644))
	busy := acquireNow(t, d, pb.CacheSharingOpt_PRIVATE)
	require.NoError(t, os.WriteFile(filepath.Join(busy.dir, "old"), []byte("x"), 0o644))
	require.NoError(t, l.release())

	o := acquireNow(t, h.lookup("other"), pb.CacheSharingOpt_LOCKED)
	require.NoError(t, os.WriteFile(filepath.Join(o.dir, "keep"), []byte("x"), 0o644))
	require.NoError(t, o.release())

	require.NoError(t, h.Prune(t.Context(), map[string]bool{"/c": false}))

	require.NoDirExists(t, l.dir, "an unused instance is reset")
	require.FileExists(t, filepath.Join(busy.dir, "old"), "an instance in use keeps its files for its holder")
	require.FileExists(t, busy.dir+hostCacheRetiredSuffix, "an instance in use is retired")
	require.FileExists(t, filepath.Join(o.dir, "keep"), "other ids are not reset")
	require.NoError(t, busy.release())

	require.NoError(t, h.Prune(t.Context(), map[string]bool{"c": false}))
	l = acquireNow(t, d, pb.CacheSharingOpt_LOCKED)
	require.Equal(t, filepath.Join(d.path, "0"), l.dir)
	require.NoFileExists(t, filepath.Join(l.dir, "old"))
	require.NoError(t, l.release())

	require.Eventually(t, func() bool {
		matches, _ := filepath.Glob(filepath.Join(d.path, hostCacheTrashPrefix+"*"))
		return len(matches) == 0
	}, 5*time.Second, 20*time.Millisecond)
}

func TestHostCacheRemovesLeftoverTrash(t *testing.T) {
	root := privateTempDir(t)
	trash := filepath.Join(root, hostCacheTrashPrefix+"leftover")
	require.NoError(t, os.MkdirAll(filepath.Join(trash, "sub"), 0o755))

	_, err := NewHostCacheMounts(map[string]string{"c": root})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		_, err := os.Stat(trash)
		return os.IsNotExist(err)
	}, 5*time.Second, 20*time.Millisecond)
}

func TestHostCacheRefSharedWithinOp(t *testing.T) {
	d := newTestHostCacheDir(t)
	mm := &MountManager{cm: nil}
	// Two LOCKED mounts of one ID in a single op must reuse the first lease
	// instead of waiting for it.
	s1 := &hostCacheShare{key: d.id, lease: acquireNow(t, d, pb.CacheSharingOpt_LOCKED), refs: 1}
	mm.hostShares = map[string]*hostCacheShare{d.id: s1}
	r1 := &hostCacheRef{share: s1, mm: mm}
	s1.refs++
	r2 := &hostCacheRef{share: s1, mm: mm}

	require.NoError(t, r1.Release(t.Context()))
	require.NoError(t, r1.Release(t.Context()), "double release is a no-op")
	ctx, cancel := context.WithTimeoutCause(t.Context(), 200*time.Millisecond, errors.WithStack(context.DeadlineExceeded))
	defer cancel()
	_, err := d.acquire(ctx, pb.CacheSharingOpt_LOCKED, nil)
	require.ErrorIs(t, err, context.DeadlineExceeded, "lease is held until the last ref is released")

	require.NoError(t, r2.Release(t.Context()))
	require.Empty(t, mm.hostShares)
	l := acquireNow(t, d, pb.CacheSharingOpt_LOCKED)
	require.NoError(t, l.release())
}

func TestHostCacheNoCacheGetsFreshInstanceWhileBusy(t *testing.T) {
	root := privateTempDir(t)
	h, err := NewHostCacheMounts(map[string]string{"c": root})
	require.NoError(t, err)
	d := h.lookup("c")

	a := acquireNow(t, d, pb.CacheSharingOpt_SHARED)
	require.NoError(t, os.WriteFile(filepath.Join(a.dir, "stale"), []byte("x"), 0o644))
	require.NoError(t, h.Prune(t.Context(), map[string]bool{"c": false}))

	for _, sharing := range []pb.CacheSharingOpt{pb.CacheSharingOpt_SHARED, pb.CacheSharingOpt_LOCKED, pb.CacheSharingOpt_PRIVATE} {
		b := acquireNow(t, d, sharing)
		require.NotEqual(t, a.dir, b.dir, "%s build after a reset must not get the retired instance", sharing)
		require.NoFileExists(t, filepath.Join(b.dir, "stale"))
		require.NoError(t, b.release())
	}
	require.FileExists(t, filepath.Join(a.dir, "stale"), "the holder keeps its files")
	require.NoError(t, a.release())

	// Once free, the retired instance is never handed out as-is: it is
	// recreated empty the next time an instance has to be created.
	held := acquireNow(t, d, pb.CacheSharingOpt_LOCKED)
	require.NotEqual(t, a.dir, held.dir)
	l := acquireNow(t, d, pb.CacheSharingOpt_PRIVATE)
	require.Equal(t, a.dir, l.dir)
	require.NoFileExists(t, filepath.Join(l.dir, "stale"))
	require.NoFileExists(t, l.dir+hostCacheRetiredSuffix)
	require.NoError(t, l.release())
	require.NoError(t, held.release())
}

func TestHostCacheSharedCreationRace(t *testing.T) {
	iterations := 100
	if testing.Short() {
		iterations = 20
	}
	for i := range iterations {
		d := newTestHostCacheDir(t)
		const n = 16
		var wg sync.WaitGroup
		leases := make([]*hostCacheLease, n)
		for j := range n {
			wg.Go(func() {
				l, err := d.acquire(t.Context(), pb.CacheSharingOpt_SHARED, nil)
				if err != nil {
					t.Error(err)
					return
				}
				leases[j] = l
			})
		}
		wg.Wait()
		for _, l := range leases {
			require.NotNil(t, l)
			require.Equal(t, leases[0].dir, l.dir, "iteration %d: shared builds split across instances", i)
			require.NoError(t, l.release())
		}
		insts, err := d.instances()
		require.NoError(t, err)
		require.Equal(t, []int{0}, insts, "iteration %d", i)
	}
}

func TestHostCacheRemovesLeftoverTmp(t *testing.T) {
	root := privateTempDir(t)
	leftover := filepath.Join(root, hostCacheTmpPrefix+"interrupted")
	require.NoError(t, os.MkdirAll(filepath.Join(leftover, "sub"), 0o755))
	_, err := NewHostCacheMounts(map[string]string{"c": root})
	require.NoError(t, err)
	require.NoDirExists(t, leftover, "removed at startup")

	d := newTestHostCacheDir(t)
	require.NoError(t, os.Mkdir(filepath.Join(d.path, hostCacheTmpPrefix+"interrupted"), 0o755))
	l := acquireNow(t, d, pb.CacheSharingOpt_SHARED)
	require.NoError(t, l.release())
	require.NoDirExists(t, filepath.Join(d.path, hostCacheTmpPrefix+"interrupted"), "removed when creating an instance")
}

func TestHostCacheRefusesNonDirectoryInstance(t *testing.T) {
	d := newTestHostCacheDir(t)
	elsewhere := t.TempDir()
	require.NoError(t, os.Symlink(elsewhere, d.instancePath(0)))
	ctx, cancel := context.WithTimeoutCause(t.Context(), time.Second, errors.WithStack(context.DeadlineExceeded))
	defer cancel()
	_, err := d.acquire(ctx, pb.CacheSharingOpt_SHARED, nil)
	require.ErrorContains(t, err, "not a directory")
}

func TestHostCacheBaseInstances(t *testing.T) {
	root := privateTempDir(t)
	h, err := NewHostCacheMounts(map[string]string{"c": root})
	require.NoError(t, err)
	d := h.lookup("c")
	uid, gid := os.Getuid(), os.Getgid()

	cacheBase := &hostCacheBase{selector: "/cache", uid: uid, gid: gid, mode: 0o750}
	rootBase := &hostCacheBase{selector: "/", uid: uid, gid: gid, mode: 0o700}
	acquireBase := func(b *hostCacheBase) *hostCacheLease {
		t.Helper()
		bd, err := d.forBase(b)
		require.NoError(t, err)
		l, err := bd.acquire(t.Context(), pb.CacheSharingOpt_SHARED, func(dir string) error {
			return initHostCacheInstance(dir, b, nil)
		})
		require.NoError(t, err)
		return l
	}

	plain := acquireNow(t, d, pb.CacheSharingOpt_SHARED)
	withCache := acquireBase(cacheBase)
	withRoot := acquireBase(rootBase)
	require.NotEqual(t, plain.dir, withCache.dir)
	require.NotEqual(t, withCache.dir, withRoot.dir)

	fi, err := os.Stat(filepath.Join(withCache.dir, "cache"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o750), fi.Mode().Perm())
	fi, err = os.Stat(withRoot.dir)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), fi.Mode().Perm())

	for _, l := range []*hostCacheLease{plain, withCache, withRoot} {
		require.NoError(t, os.WriteFile(filepath.Join(l.dir, "f"), nil, 0o644))
		require.NoError(t, l.release())
	}
	require.NoError(t, h.Prune(t.Context(), map[string]bool{"c": false}))
	for _, l := range []*hostCacheLease{plain, withCache, withRoot} {
		require.NoDirExists(t, l.dir, "reset covers instances of every base")
	}
}

func TestHostCacheAcquireDuringResetNeverFails(t *testing.T) {
	root := privateTempDir(t)
	h, err := NewHostCacheMounts(map[string]string{"c": root})
	require.NoError(t, err)
	d := h.lookup("c")

	duration := time.Second
	if testing.Short() {
		duration = 200 * time.Millisecond
	}
	ctx, cancel := context.WithTimeoutCause(t.Context(), duration, errors.WithStack(context.DeadlineExceeded))
	defer cancel()

	var wg sync.WaitGroup
	var acquires, failures atomic.Int64
	modes := []pb.CacheSharingOpt{pb.CacheSharingOpt_SHARED, pb.CacheSharingOpt_LOCKED, pb.CacheSharingOpt_PRIVATE}
	for i := range 8 {
		wg.Go(func() {
			for context.Cause(ctx) == nil {
				l, err := d.acquire(ctx, modes[i%len(modes)], nil)
				if err != nil {
					if context.Cause(ctx) == nil {
						failures.Add(1)
						t.Errorf("acquire failed during concurrent resets: %v", err)
					}
					return
				}
				acquires.Add(1)
				require.NoError(t, l.release())
			}
		})
	}
	wg.Go(func() {
		for context.Cause(ctx) == nil {
			if err := h.Prune(t.Context(), map[string]bool{"c": false}); err != nil {
				t.Errorf("prune: %v", err)
				return
			}
		}
	})
	wg.Wait()
	require.Zero(t, failures.Load())
	require.Positive(t, acquires.Load())
}

func TestHostCacheLockedCreationRace(t *testing.T) {
	// LOCKED waiters poll, so each round takes a few hundred milliseconds.
	rounds := 10
	if testing.Short() {
		rounds = 3
	}
	for i := range rounds {
		d := newTestHostCacheDir(t)
		var holders, maxHolders atomic.Int32
		var wg sync.WaitGroup
		for range 6 {
			wg.Go(func() {
				l, err := d.acquire(t.Context(), pb.CacheSharingOpt_LOCKED, nil)
				if err != nil {
					t.Error(err)
					return
				}
				n := holders.Add(1)
				for {
					m := maxHolders.Load()
					if n <= m || maxHolders.CompareAndSwap(m, n) {
						break
					}
				}
				time.Sleep(time.Millisecond)
				holders.Add(-1)
				require.NoError(t, l.release())
			})
		}
		wg.Wait()
		require.EqualValues(t, 1, maxHolders.Load(), "round %d: locked builds ran concurrently", i)
		insts, err := d.instances()
		require.NoError(t, err)
		require.Equal(t, []int{0}, insts, "round %d", i)
	}
}

func TestHostCacheRetiredRemovedOnRelease(t *testing.T) {
	root := privateTempDir(t)
	h, err := NewHostCacheMounts(map[string]string{"c": root})
	require.NoError(t, err)
	d := h.lookup("c")

	a := acquireNow(t, d, pb.CacheSharingOpt_SHARED)
	a2 := acquireNow(t, d, pb.CacheSharingOpt_SHARED)
	require.Equal(t, a.dir, a2.dir)
	require.NoError(t, h.Prune(t.Context(), map[string]bool{"c": false}))
	b := acquireNow(t, d, pb.CacheSharingOpt_SHARED)
	require.NotEqual(t, a.dir, b.dir)

	require.NoError(t, a.release())
	require.DirExists(t, a2.dir, "still held by another build")
	require.NoError(t, a2.release())
	require.NoDirExists(t, a2.dir, "the last holder removes the retired instance")
	require.DirExists(t, b.dir)
	require.NoError(t, b.release())
}

func TestHostCacheRetiredRemovedAtStartup(t *testing.T) {
	root := privateTempDir(t)
	require.NoError(t, os.Mkdir(filepath.Join(root, "0"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "0", "data"), nil, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "0"+hostCacheRetiredSuffix), nil, 0o600))
	require.NoError(t, os.Mkdir(filepath.Join(root, "1"), 0o755))

	_, err := NewHostCacheMounts(map[string]string{"c": root})
	require.NoError(t, err)
	require.NoDirExists(t, filepath.Join(root, "0"))
	require.DirExists(t, filepath.Join(root, "1"), "instances that are not retired stay")
}

func TestInitHostCacheInstanceIdentityMapping(t *testing.T) {
	uid, gid := os.Getuid(), os.Getgid()
	remappedRoot := uid
	baseUID, baseGID := uid, gid
	if os.Geteuid() == 0 {
		// As root, map to IDs that differ from the base's to tell them apart.
		remappedRoot, baseUID, baseGID = 100000, 1000, 1001
	}
	idmap := &user.IdentityMapping{
		UIDMaps: []user.IDMap{{ID: 0, ParentID: int64(remappedRoot), Count: 1}},
		GIDMaps: []user.IDMap{{ID: 0, ParentID: int64(remappedRoot), Count: 1}},
	}
	if os.Geteuid() != 0 {
		idmap.GIDMaps[0].ParentID = int64(gid)
	}
	rootUID, rootGID := idmap.RootPair()

	owner := func(p string) (int, int, uint32) {
		t.Helper()
		fi, err := os.Lstat(p)
		require.NoError(t, err)
		u, g, mode, err := fileOwnerAndMode(fi)
		require.NoError(t, err)
		return u, g, mode
	}

	plain := t.TempDir()
	require.NoError(t, initHostCacheInstance(plain, nil, idmap))
	u, g, _ := owner(plain)
	require.Equal(t, []int{rootUID, rootGID}, []int{u, g}, "a plain instance is owned by the remapped root")

	withBase := t.TempDir()
	base := &hostCacheBase{selector: "/a/b", uid: baseUID, gid: baseGID, mode: 0o750}
	require.NoError(t, initHostCacheInstance(withBase, base, idmap))
	u, g, _ = owner(withBase)
	require.Equal(t, []int{rootUID, rootGID}, []int{u, g}, "instance root")
	u, g, _ = owner(filepath.Join(withBase, "a"))
	require.Equal(t, []int{rootUID, rootGID}, []int{u, g}, "intermediate directory")
	u, g, mode := owner(filepath.Join(withBase, "a", "b"))
	require.Equal(t, []int{baseUID, baseGID}, []int{u, g}, "selected directory keeps the base's owner")
	require.Equal(t, uint32(0o750), mode)
}

func TestHostCacheRetiredMarkerRemovedWithInstance(t *testing.T) {
	root := privateTempDir(t)
	h, err := NewHostCacheMounts(map[string]string{"c": root})
	require.NoError(t, err)
	d := h.lookup("c")

	held := acquireNow(t, d, pb.CacheSharingOpt_SHARED)
	require.NoError(t, h.Prune(t.Context(), map[string]bool{"c": false}))
	require.FileExists(t, held.dir+hostCacheRetiredSuffix)
	require.NoError(t, held.release())
	require.NoDirExists(t, held.dir)
	require.NoFileExists(t, held.dir+hostCacheRetiredSuffix, "the marker goes with the instance")

	// An operator seeding the same number later keeps their data, also across a restart.
	require.NoError(t, os.Mkdir(held.dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(held.dir, "seed"), nil, 0o644))
	h, err = NewHostCacheMounts(map[string]string{"c": root})
	require.NoError(t, err)
	l := acquireNow(t, h.lookup("c"), pb.CacheSharingOpt_SHARED)
	require.Equal(t, held.dir, l.dir)
	require.FileExists(t, filepath.Join(l.dir, "seed"))
	require.NoError(t, l.release())
}

func TestHostCacheRetiredRemovedWhenHoldersReleaseTogether(t *testing.T) {
	rounds := 300
	if testing.Short() {
		rounds = 30
	}
	for i := range rounds {
		root := privateTempDir(t)
		h, err := NewHostCacheMounts(map[string]string{"c": root})
		require.NoError(t, err)
		d := h.lookup("c")
		leases := []*hostCacheLease{
			acquireNow(t, d, pb.CacheSharingOpt_SHARED),
			acquireNow(t, d, pb.CacheSharingOpt_SHARED),
			acquireNow(t, d, pb.CacheSharingOpt_SHARED),
		}
		require.NoError(t, h.Prune(t.Context(), map[string]bool{"c": false}))

		start := make(chan struct{})
		var wg sync.WaitGroup
		for _, l := range leases {
			wg.Go(func() {
				<-start
				require.NoError(t, l.release())
			})
		}
		close(start)
		wg.Wait()
		require.NoDirExists(t, leases[0].dir, "round %d: holders released together and left the retired instance", i)
	}
}

func TestHostCacheConcurrentResetsLeaveNoStrayMarker(t *testing.T) {
	rounds := 500
	if testing.Short() {
		rounds = 100
	}
	root := privateTempDir(t)
	h, err := NewHostCacheMounts(map[string]string{"c": root})
	require.NoError(t, err)
	d := h.lookup("c")
	for i := range rounds {
		l := acquireNow(t, d, pb.CacheSharingOpt_SHARED)
		require.NoError(t, l.release())

		start := make(chan struct{})
		var wg sync.WaitGroup
		for range 3 {
			wg.Go(func() {
				<-start
				if err := h.Prune(t.Context(), map[string]bool{"c": false}); err != nil {
					t.Error(err)
				}
			})
		}
		close(start)
		wg.Wait()
		for _, n := range []int{0, 1} {
			if _, err := os.Lstat(d.instancePath(n)); errors.Is(err, os.ErrNotExist) {
				require.NoFileExists(t, d.instancePath(n)+hostCacheRetiredSuffix, "round %d: marker without its instance", i)
			}
		}
	}
}

func TestHostCacheStartupRemovesStrayMarker(t *testing.T) {
	root := privateTempDir(t)
	require.NoError(t, os.WriteFile(filepath.Join(root, "0"+hostCacheRetiredSuffix), nil, 0o600))
	_, err := NewHostCacheMounts(map[string]string{"c": root})
	require.NoError(t, err)
	require.NoFileExists(t, filepath.Join(root, "0"+hostCacheRetiredSuffix))
}

// holdMarkerLock simulates a peer daemon that stalls while holding the marker
// lock of instance n.
func holdMarkerLock(t *testing.T, d *hostCacheDir, n int) func() {
	t.Helper()
	f, err := openLockFile(d.instancePath(n) + hostCacheMarkerLock)
	require.NoError(t, err)
	locked, err := tryLockFile(f, true)
	require.NoError(t, err)
	require.True(t, locked)
	return func() {
		unlockFile(f)
		f.Close()
	}
}

func TestHostCacheMarkerLockWaitsAreBounded(t *testing.T) {
	old := hostCacheCleanupWait
	hostCacheCleanupWait = 200 * time.Millisecond
	t.Cleanup(func() { hostCacheCleanupWait = old })

	root := privateTempDir(t)
	h, err := NewHostCacheMounts(map[string]string{"c": root})
	require.NoError(t, err)
	d := h.lookup("c")
	held := acquireNow(t, d, pb.CacheSharingOpt_SHARED)
	unlock := holdMarkerLock(t, d, 0)

	// A reset of a busy instance honors its context.
	ctx, cancel := context.WithTimeoutCause(t.Context(), 200*time.Millisecond, errors.WithStack(context.DeadlineExceeded))
	start := time.Now()
	err = h.Prune(ctx, map[string]bool{"c": false})
	cancel()
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(start), 2*time.Second)

	// Retire it for real, then stall again: releasing the last lease gives up
	// after the cleanup bound and leaves the retired instance for later.
	unlock()
	require.NoError(t, h.Prune(t.Context(), map[string]bool{"c": false}))
	unlock = holdMarkerLock(t, d, 0)
	start = time.Now()
	require.NoError(t, held.release())
	require.Less(t, time.Since(start), 2*time.Second)
	require.DirExists(t, held.dir)

	// An acquire that has to recreate the retired instance honors its context.
	ctx, cancel = context.WithTimeoutCause(t.Context(), 200*time.Millisecond, errors.WithStack(context.DeadlineExceeded))
	start = time.Now()
	_, err = d.acquire(ctx, pb.CacheSharingOpt_LOCKED, nil)
	cancel()
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(start), 2*time.Second)

	// Once the peer moves on, the next build cleans up and gets a fresh instance.
	unlock()
	l := acquireNow(t, d, pb.CacheSharingOpt_LOCKED)
	require.Equal(t, held.dir, l.dir)
	require.NoFileExists(t, l.dir+hostCacheRetiredSuffix)
	require.NoError(t, l.release())
}
