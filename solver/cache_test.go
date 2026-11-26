package solver

import (
	"context"
	"errors"
	"io"
	"maps"
	"sync"
	"testing"
	"time"

	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/util/bklog"
	digest "github.com/opencontainers/go-digest"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

func depKeys(cks ...ExportableCacheKey) []CacheKeyWithSelector {
	var keys []CacheKeyWithSelector
	for _, ck := range cks {
		keys = append(keys, CacheKeyWithSelector{CacheKey: ck})
	}
	return keys
}

func testCacheKey(dgst digest.Digest, output Index, deps ...ExportableCacheKey) *CacheKey {
	k := NewCacheKey(dgst, "", output)
	k.deps = make([][]CacheKeyWithSelector, len(deps))
	for i, dep := range deps {
		k.deps[i] = depKeys(dep)
	}
	return k
}

func testCacheKeyWithDeps(dgst digest.Digest, output Index, deps [][]CacheKeyWithSelector) *CacheKey {
	k := NewCacheKey(dgst, "", output)
	k.deps = deps
	return k
}

func expKey(k *CacheKey) ExportableCacheKey {
	return ExportableCacheKey{CacheKey: k, Exporter: &exporter{k: k}}
}

func TestInMemoryCache(t *testing.T) {
	ctx := t.Context()

	m := NewInMemoryCacheManager()

	cacheFoo, err := m.Save(NewCacheKey(dgst("foo"), "", 0), testResult("result0"), time.Now())
	require.NoError(t, err)

	keys, err := m.Query(nil, 0, dgst("foo"), 0)
	require.NoError(t, err)
	require.Equal(t, 1, len(keys))

	matches, err := m.Records(ctx, keys[0])
	require.NoError(t, err)
	require.Equal(t, 1, len(matches))

	res, err := m.Load(ctx, matches[0])
	require.NoError(t, err)
	require.Equal(t, "result0", unwrap(res))

	// another record
	cacheBar, err := m.Save(NewCacheKey(dgst("bar"), "", 0), testResult("result1"), time.Now())
	require.NoError(t, err)

	keys, err = m.Query(nil, 0, dgst("bar"), 0)
	require.NoError(t, err)
	require.Equal(t, 1, len(keys))

	matches, err = m.Records(ctx, keys[0])
	require.NoError(t, err)
	require.Equal(t, 1, len(matches))

	res, err = m.Load(ctx, matches[0])
	require.NoError(t, err)
	require.Equal(t, "result1", unwrap(res))

	// invalid request
	keys, err = m.Query(nil, 0, dgst("baz"), 0)
	require.NoError(t, err)
	require.Equal(t, 0, len(keys))

	// second level
	k := testCacheKey(dgst("baz"), Index(1), *cacheFoo, *cacheBar)
	cacheBaz, err := m.Save(k, testResult("result2"), time.Now())
	require.NoError(t, err)

	keys, err = m.Query(nil, 0, dgst("baz"), 0)
	require.NoError(t, err)
	require.Equal(t, 0, len(keys))

	keys, err = m.Query(depKeys(*cacheFoo), 0, dgst("baz"), 0)
	require.NoError(t, err)
	require.Equal(t, 0, len(keys))

	keys, err = m.Query(depKeys(*cacheFoo), 1, dgst("baz"), Index(1))
	require.NoError(t, err)
	require.Equal(t, 0, len(keys))

	keys, err = m.Query(depKeys(*cacheFoo), 0, dgst("baz"), Index(1))
	require.NoError(t, err)
	require.Equal(t, 1, len(keys))

	matches, err = m.Records(ctx, keys[0])
	require.NoError(t, err)
	require.Equal(t, 1, len(matches))

	res, err = m.Load(ctx, matches[0])
	require.NoError(t, err)
	require.Equal(t, "result2", unwrap(res))

	keys2, err := m.Query(depKeys(*cacheBar), 1, dgst("baz"), Index(1))
	require.NoError(t, err)
	require.Equal(t, 1, len(keys2))

	require.Equal(t, keys[0].ID, keys2[0].ID)

	k = testCacheKey(dgst("baz"), Index(1), *cacheFoo)
	_, err = m.Save(k, testResult("result3"), time.Now())
	require.NoError(t, err)

	keys, err = m.Query(depKeys(*cacheFoo), 0, dgst("baz"), Index(1))
	require.NoError(t, err)
	require.Equal(t, 1, len(keys))

	matches, err = m.Records(ctx, keys[0])
	require.NoError(t, err)
	require.Equal(t, 2, len(matches))

	k = testCacheKeyWithDeps(dgst("bax"), 0, [][]CacheKeyWithSelector{
		{{CacheKey: *cacheFoo}, {CacheKey: *cacheBaz}},
		{{CacheKey: *cacheBar}},
	})
	_, err = m.Save(k, testResult("result4"), time.Now())
	require.NoError(t, err)

	// foo, bar, baz should all point to result4
	keys, err = m.Query(depKeys(*cacheFoo), 0, dgst("bax"), 0)
	require.NoError(t, err)
	require.Equal(t, 1, len(keys))

	id := keys[0].ID

	keys, err = m.Query(depKeys(*cacheBar), 1, dgst("bax"), 0)
	require.NoError(t, err)
	require.Equal(t, 1, len(keys))
	require.Equal(t, keys[0].ID, id)

	keys, err = m.Query(depKeys(*cacheBaz), 0, dgst("bax"), 0)
	require.NoError(t, err)
	require.Equal(t, 1, len(keys))
	require.Equal(t, keys[0].ID, id)
}

func TestInMemoryCacheSelector(t *testing.T) {
	ctx := t.Context()

	m := NewInMemoryCacheManager()

	cacheFoo, err := m.Save(NewCacheKey(dgst("foo"), "", 0), testResult("result0"), time.Now())
	require.NoError(t, err)

	_, err = m.Save(testCacheKeyWithDeps(dgst("bar"), 0, [][]CacheKeyWithSelector{
		{{CacheKey: *cacheFoo, Selector: dgst("sel0")}},
	}), testResult("result1"), time.Now())
	require.NoError(t, err)

	keys, err := m.Query(depKeys(*cacheFoo), 0, dgst("bar"), 0)
	require.NoError(t, err)
	require.Equal(t, 0, len(keys))

	keys, err = m.Query([]CacheKeyWithSelector{{Selector: "sel-invalid", CacheKey: *cacheFoo}}, 0, dgst("bar"), 0)
	require.NoError(t, err)
	require.Equal(t, 0, len(keys))

	keys, err = m.Query([]CacheKeyWithSelector{{Selector: dgst("sel0"), CacheKey: *cacheFoo}}, 0, dgst("bar"), 0)
	require.NoError(t, err)
	require.Equal(t, 1, len(keys))

	matches, err := m.Records(ctx, keys[0])
	require.NoError(t, err)
	require.Equal(t, 1, len(matches))

	res, err := m.Load(ctx, matches[0])
	require.NoError(t, err)
	require.Equal(t, "result1", unwrap(res))
}

func TestInMemoryCacheSelectorNested(t *testing.T) {
	ctx := t.Context()

	m := NewInMemoryCacheManager()

	cacheFoo, err := m.Save(NewCacheKey(dgst("foo"), "", 0), testResult("result0"), time.Now())
	require.NoError(t, err)

	_, err = m.Save(testCacheKeyWithDeps(dgst("bar"), 0, [][]CacheKeyWithSelector{
		{{CacheKey: *cacheFoo, Selector: dgst("sel0")}, {CacheKey: expKey(NewCacheKey(dgst("second"), "", 0))}},
	}), testResult("result1"), time.Now())
	require.NoError(t, err)

	keys, err := m.Query(
		[]CacheKeyWithSelector{{Selector: dgst("sel0"), CacheKey: *cacheFoo}},
		0, dgst("bar"), 0)
	require.NoError(t, err)
	require.Equal(t, 1, len(keys))

	matches, err := m.Records(ctx, keys[0])
	require.NoError(t, err)
	require.Equal(t, 1, len(matches))

	res, err := m.Load(ctx, matches[0])
	require.NoError(t, err)
	require.Equal(t, "result1", unwrap(res))

	keys, err = m.Query(depKeys(*cacheFoo), 0, dgst("bar"), 0)
	require.NoError(t, err)
	require.Equal(t, 0, len(keys))

	keys, err = m.Query([]CacheKeyWithSelector{{Selector: dgst("bar"), CacheKey: *cacheFoo}}, 0, dgst("bar"), 0)
	require.NoError(t, err)
	require.Equal(t, 0, len(keys))

	keys, err = m.Query(depKeys(expKey(NewCacheKey(dgst("second"), "", 0))), 0, dgst("bar"), 0)
	require.NoError(t, err)
	require.Equal(t, 1, len(keys))

	matches, err = m.Records(ctx, keys[0])
	require.NoError(t, err)
	require.Equal(t, 1, len(matches))

	res, err = m.Load(ctx, matches[0])
	require.NoError(t, err)
	require.Equal(t, "result1", unwrap(res))

	keys, err = m.Query(depKeys(expKey(NewCacheKey(dgst("second"), "", 0))), 0, dgst("bar"), 0)
	require.NoError(t, err)
	require.Equal(t, 1, len(keys))
}

func TestInMemoryCacheReleaseParent(t *testing.T) {
	storage := NewInMemoryCacheStorage()
	results := NewInMemoryResultStorage()
	ctx := t.Context()
	m := NewCacheManager(ctx, identity.NewID(), storage, results)

	res0 := testResult("result0")
	cacheFoo, err := m.Save(NewCacheKey(dgst("foo"), "", 0), res0, time.Now())
	require.NoError(t, err)

	res1 := testResult("result1")
	_, err = m.Save(testCacheKey(dgst("bar"), 0, *cacheFoo), res1, time.Now())
	require.NoError(t, err)

	keys, err := m.Query(nil, 0, dgst("foo"), 0)
	require.NoError(t, err)
	require.Equal(t, 1, len(keys))

	matches, err := m.Records(ctx, keys[0])
	require.NoError(t, err)
	require.Equal(t, 1, len(matches))

	err = storage.Release(res0.ID())
	require.NoError(t, err)

	// foo becomes unloadable
	keys, err = m.Query(nil, 0, dgst("foo"), 0)
	require.NoError(t, err)
	require.Equal(t, 1, len(keys))

	matches, err = m.Records(ctx, keys[0])
	require.NoError(t, err)
	require.Equal(t, 0, len(matches))

	keys, err = m.Query(depKeys(expKey(keys[0])), 0, dgst("bar"), 0)
	require.NoError(t, err)
	require.Equal(t, 1, len(keys))

	matches, err = m.Records(ctx, keys[0])
	require.NoError(t, err)
	require.Equal(t, 1, len(matches))

	// releasing bar releases both foo and bar
	err = storage.Release(res1.ID())
	require.NoError(t, err)

	keys, err = m.Query(nil, 0, dgst("foo"), 0)
	require.NoError(t, err)
	require.Equal(t, 0, len(keys))
}

// TestInMemoryCacheRestoreOfflineDeletion deletes a result while the
// cachemanager is not running and checks that it syncs up on restore
func TestInMemoryCacheRestoreOfflineDeletion(t *testing.T) {
	storage := NewInMemoryCacheStorage()
	results := NewInMemoryResultStorage()
	ctx := t.Context()
	m := NewCacheManager(ctx, identity.NewID(), storage, results)

	res0 := testResult("result0")
	cacheFoo, err := m.Save(NewCacheKey(dgst("foo"), "", 0), res0, time.Now())
	require.NoError(t, err)

	res1 := testResult("result1")
	_, err = m.Save(testCacheKey(dgst("bar"), 0, *cacheFoo), res1, time.Now())
	require.NoError(t, err)

	results2 := NewInMemoryResultStorage()
	_, err = results2.Save(res1, time.Now()) // only add bar
	require.NoError(t, err)

	m = NewCacheManager(ctx, identity.NewID(), storage, results2)

	keys, err := m.Query(nil, 0, dgst("foo"), 0)
	require.NoError(t, err)
	require.Equal(t, 1, len(keys))

	matches, err := m.Records(ctx, keys[0])
	require.NoError(t, err)
	require.Equal(t, 0, len(matches))

	keys, err = m.Query(depKeys(expKey(keys[0])), 0, dgst("bar"), 0)
	require.NoError(t, err)
	require.Equal(t, 1, len(keys))

	matches, err = m.Records(ctx, keys[0])
	require.NoError(t, err)
	require.Equal(t, 1, len(matches))
}

func TestCarryOverFromSublink(t *testing.T) {
	storage := NewInMemoryCacheStorage()
	results := NewInMemoryResultStorage()
	ctx := t.Context()
	m := NewCacheManager(ctx, identity.NewID(), storage, results)

	cacheFoo, err := m.Save(NewCacheKey(dgst("foo"), "", 0), testResult("resultFoo"), time.Now())
	require.NoError(t, err)

	_, err = m.Save(testCacheKeyWithDeps(dgst("res"), 0, [][]CacheKeyWithSelector{
		{{CacheKey: *cacheFoo, Selector: dgst("sel0")}, {CacheKey: expKey(NewCacheKey(dgst("content0"), "", 0))}},
	}), testResult("result0"), time.Now())
	require.NoError(t, err)

	cacheBar, err := m.Save(NewCacheKey(dgst("bar"), "", 0), testResult("resultBar"), time.Now())
	require.NoError(t, err)

	keys, err := m.Query([]CacheKeyWithSelector{
		{CacheKey: *cacheBar, Selector: dgst("sel0")},
		{CacheKey: expKey(NewCacheKey(dgst("content0"), "", 0))},
	}, 0, dgst("res"), 0)
	require.NoError(t, err)
	require.Equal(t, 1, len(keys))

	keys, err = m.Query([]CacheKeyWithSelector{
		{Selector: dgst("sel0"), CacheKey: *cacheBar},
	}, 0, dgst("res"), 0)
	require.NoError(t, err)
	require.Equal(t, 1, len(keys))
}

func dgst(s string) digest.Digest {
	return digest.FromBytes([]byte(s))
}

func testResult(v string) Result {
	return &dummyResult{
		id:    identity.NewID(),
		value: v,
	}
}

func unwrap(res Result) string {
	r, ok := res.Sys().(*dummyResult)
	if !ok {
		return "unwrap-error"
	}
	return r.value
}

type captureLogHook struct {
	mu      sync.Mutex
	entries []*logrus.Entry
}

func (h *captureLogHook) Levels() []logrus.Level {
	return logrus.AllLevels
}

func (h *captureLogHook) Fire(e *logrus.Entry) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	cp := *e
	cp.Data = maps.Clone(e.Data)
	h.entries = append(h.entries, &cp)
	return nil
}

type releaseLogTestStorage struct {
	CacheKeyStorage
	walk    func(func(string) error) error
	release func(string) error
}

func (s *releaseLogTestStorage) Walk(fn func(string) error) error {
	if s.walk != nil {
		return s.walk(fn)
	}
	return s.CacheKeyStorage.Walk(fn)
}

func (s *releaseLogTestStorage) Release(id string) error {
	if s.release != nil {
		return s.release(id)
	}
	return s.CacheKeyStorage.Release(id)
}

type releaseLogTestResults struct {
	CacheResultStorage
	exists func(context.Context, string) bool
}

func (s *releaseLogTestResults) Exists(ctx context.Context, id string) bool {
	return s.exists(ctx, id)
}

func TestReleaseUnreferencedLogging(t *testing.T) {
	newLogCtx := func(t *testing.T) (context.Context, *captureLogHook) {
		t.Helper()
		logger := logrus.New()
		logger.SetOutput(io.Discard)
		logger.SetLevel(logrus.DebugLevel)
		hook := &captureLogHook{}
		logger.AddHook(hook)
		return bklog.WithLogger(t.Context(), logrus.NewEntry(logger)), hook
	}

	t.Run("released and deduplicated", func(t *testing.T) {
		ctx, hook := newLogCtx(t)
		storage := NewInMemoryCacheStorage()
		require.NoError(t, storage.AddResult("k1", CacheResult{ID: "live"}))
		require.NoError(t, storage.AddResult("k2", CacheResult{ID: "missing"}))
		require.NoError(t, storage.AddResult("k3", CacheResult{ID: "missing"}))

		cm := &cacheManager{
			id:      "test-cm",
			backend: storage,
			results: &releaseLogTestResults{exists: func(_ context.Context, id string) bool {
				return id == "live"
			}},
		}
		require.NoError(t, cm.ReleaseUnreferenced(ctx))

		require.Len(t, hook.entries, 1)
		entry := hook.entries[0]
		require.Equal(t, logrus.DebugLevel, entry.Level)
		require.Equal(t, "released unreferenced cache metadata", entry.Message)
		require.Equal(t, "test-cm", entry.Data["cache_manager"])
		require.Equal(t, 2, entry.Data["checked"])
		require.Equal(t, 1, entry.Data["released"])
		require.Contains(t, entry.Data, "duration")
		require.Contains(t, entry.Data, "release_duration")
		require.Contains(t, entry.Data, "max_release_duration")
		require.NotContains(t, entry.Data, "failed")
		require.NotContains(t, entry.Data, logrus.ErrorKey)
	})

	t.Run("not found ignored and release error warned", func(t *testing.T) {
		ctx, hook := newLogCtx(t)
		storage := NewInMemoryCacheStorage()
		require.NoError(t, storage.AddResult("k1", CacheResult{ID: "gone"}))
		require.NoError(t, storage.AddResult("k2", CacheResult{ID: "broken"}))
		require.NoError(t, storage.AddResult("k3", CacheResult{ID: "ok"}))

		boom := errors.New("storage failure")
		cm := &cacheManager{
			id: "test-cm",
			backend: &releaseLogTestStorage{
				CacheKeyStorage: storage,
				release: func(id string) error {
					switch id {
					case "gone":
						return ErrNotFound
					case "broken":
						return boom
					default:
						return storage.Release(id)
					}
				},
			},
			results: &releaseLogTestResults{exists: func(context.Context, string) bool {
				return false
			}},
		}
		require.NoError(t, cm.ReleaseUnreferenced(ctx))

		require.Len(t, hook.entries, 2)
		warnEntry, debugEntry := hook.entries[0], hook.entries[1]
		require.Equal(t, logrus.WarnLevel, warnEntry.Level)
		require.Contains(t, warnEntry.Message, "failed to release 1 unreferenced cache results")
		warnErr, ok := warnEntry.Data[logrus.ErrorKey].(error)
		require.True(t, ok)
		require.ErrorIs(t, warnErr, boom)

		require.Equal(t, logrus.DebugLevel, debugEntry.Level)
		require.Equal(t, 3, debugEntry.Data["checked"])
		require.Equal(t, 1, debugEntry.Data["released"])
		require.Equal(t, 1, debugEntry.Data["failed"])
		require.NotContains(t, debugEntry.Data, logrus.ErrorKey)
	})

	t.Run("walk error logged", func(t *testing.T) {
		ctx, hook := newLogCtx(t)
		walkErr := errors.New("walk failed")
		cm := &cacheManager{
			id: "test-cm",
			backend: &releaseLogTestStorage{
				CacheKeyStorage: NewInMemoryCacheStorage(),
				walk: func(func(string) error) error {
					return walkErr
				},
			},
			results: &releaseLogTestResults{exists: func(context.Context, string) bool {
				return true
			}},
		}
		require.ErrorIs(t, cm.ReleaseUnreferenced(ctx), walkErr)

		require.Len(t, hook.entries, 1)
		entry := hook.entries[0]
		require.Equal(t, logrus.DebugLevel, entry.Level)
		entryErr, ok := entry.Data[logrus.ErrorKey].(error)
		require.True(t, ok)
		require.ErrorIs(t, entryErr, walkErr)
		require.NotContains(t, entry.Data, "release_duration")
	})
}
