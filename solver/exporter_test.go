package solver

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/moby/buildkit/identity"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/util/compression"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

func TestCompareCacheRecord(t *testing.T) {
	now := time.Now()
	a := &CacheRecord{CreatedAt: now, Priority: 1}
	b := &CacheRecord{CreatedAt: now, Priority: 2}
	c := &CacheRecord{CreatedAt: now.Add(1 * time.Second), Priority: 1}
	d := &CacheRecord{CreatedAt: now.Add(-1 * time.Second), Priority: 1}

	records := []*CacheRecord{b, nil, d, a, c, nil}
	slices.SortFunc(records, compareCacheRecord)

	names := map[*CacheRecord]string{
		a:   "a",
		b:   "b",
		c:   "c",
		d:   "d",
		nil: "nil",
	}
	var got []string
	for _, r := range records {
		got = append(got, names[r])
	}
	want := []string{"c", "a", "b", "d", "nil", "nil"}
	if !slices.Equal(got, want) {
		t.Fatalf("unexpected order: got %v, want %v", got, want)
	}
}

// lazyOptKey is a stand-in for cache.DescHandlerKey: an opaque cache opt key
// that identifies a lazy blob for which a descriptor handler (provider) must
// be resolvable through the context cache-opt getter before the blob can be
// loaded.
type lazyOptKey digest.Digest

// cacheOptsCtx wraps ctx with a cache-opt getter that resolves exactly the
// given keys, mimicking the record-specific ancestor getter installed by
// withAncestorCacheOpts for one vertex state.
func cacheOptsCtx(known map[lazyOptKey]any) func(context.Context) context.Context {
	return func(ctx context.Context) context.Context {
		return WithCacheOptGetter(ctx, func(_ bool, keys ...any) map[any]any {
			vals := make(map[any]any)
			for _, k := range keys {
				if key, ok := k.(lazyOptKey); ok {
					if v, ok := known[key]; ok {
						vals[key] = v
					}
				}
			}
			return vals
		})
	}
}

// outerGetter returns a cache-opt getter resolving exactly the given keys,
// mimicking the caller-installed getter (withDescHandlerCacheOpts) that only
// covers the final result ref chain.
func outerGetter(known map[lazyOptKey]any) func(bool, ...any) map[any]any {
	return func(_ bool, keys ...any) map[any]any {
		vals := make(map[any]any)
		for _, k := range keys {
			if key, ok := k.(lazyOptKey); ok {
				if v, ok := known[key]; ok {
					vals[key] = v
				}
			}
		}
		return vals
	}
}

// lazyResultStore simulates worker result storage backed by a lazy snapshotter:
// results registered in the lazy map can only be loaded when the context
// cache-opt getter resolves their lazyOptKey (mirroring how LoadRef recovers
// DescHandlers via CacheOptGetterOf before retrying, and how LoadRemotes
// swallows the failure best-effort and falls back to a local Load).
type lazyResultStore struct {
	CacheResultStorage
	lazy map[string]lazyOptKey // result ID -> required cache opt key
}

func (s *lazyResultStore) resolvable(ctx context.Context, key lazyOptKey) bool {
	if g := CacheOptGetterOf(ctx); g != nil {
		_, ok := g(true, key)[key]
		return ok
	}
	return false
}

func (s *lazyResultStore) Load(ctx context.Context, res CacheResult) (Result, error) {
	if key, ok := s.lazy[res.ID]; ok && !s.resolvable(ctx, key) {
		return nil, fmt.Errorf("lazy blob %s: no descriptor handler available", digest.Digest(key))
	}
	return s.CacheResultStorage.Load(ctx, res)
}

func (s *lazyResultStore) LoadRemotes(ctx context.Context, res CacheResult, compressionopt *compression.Config, g session.Group) ([]*Remote, error) {
	if key, ok := s.lazy[res.ID]; ok {
		if !s.resolvable(ctx, key) {
			// mirror worker/cacheresult.go: loadRemote is best effort
			return nil, nil
		}
		return []*Remote{{Descriptors: []ocispecs.Descriptor{{Digest: digest.Digest(key)}}}}, nil
	}
	return s.CacheResultStorage.LoadRemotes(ctx, res, compressionopt, g)
}

// testCacheTarget collects records added by the export.
type testCacheTarget struct {
	adds []testCacheTargetAdd
}

type testCacheTargetAdd struct {
	dgst    digest.Digest
	deps    [][]CacheLink
	results []CacheExportResult
}

func (t *testCacheTarget) Add(dgst digest.Digest, deps [][]CacheLink, results []CacheExportResult) (CacheExporterRecord, bool, error) {
	t.adds = append(t.adds, testCacheTargetAdd{dgst: dgst, deps: deps, results: results})
	return &CacheExporterRecordBase{}, true, nil
}

// exportedBlobDigests returns the remote blob digests attached to records
// added to the target.
func (t *testCacheTarget) exportedBlobDigests() map[digest.Digest]struct{} {
	out := map[digest.Digest]struct{}{}
	for _, a := range t.adds {
		for _, r := range a.results {
			if r.Result == nil {
				continue
			}
			for _, d := range r.Result.Descriptors {
				out[d.Digest] = struct{}{}
			}
		}
	}
	return out
}

func testExportOpt() CacheExportOpt {
	return CacheExportOpt{
		Mode:            CacheExportModeMax,
		IgnoreBacklinks: true,
		ResolveRemotes: func(context.Context, Result) ([]*Remote, error) {
			return nil, nil
		},
	}
}

// TestExportToLazyCrossStageRecordsUseTheirOwnCacheOpts covers the
// cross-stage cache export regression from
// https://github.com/moby/buildkit/issues/6893.
//
// The outer export context carries a cache-opt getter that only knows the
// lazy blob of the final (root) record, like the withDescHandlerCacheOpts
// getter the cache exporter installs for the final result ref. An
// intermediate record from an earlier build stage (here: the dependency of
// the root, itself depending on a base record) has a lazy blob that is only
// resolvable through the record-specific ancestor getter (recordCtxOpts).
//
// ExportTo must re-root the getter at each record's own state so that the
// intermediate record can resolve its lazy blob. Before the fix, the
// pre-existing getter made ExportTo skip re-rooting, the intermediate record
// failed to load and was silently dropped together with the root record.
func TestExportToLazyCrossStageRecordsUseTheirOwnCacheOpts(t *testing.T) {
	ctx := t.Context()

	blobMid := lazyOptKey(digest.FromBytes([]byte("mid-blob")))
	blobRoot := lazyOptKey(digest.FromBytes([]byte("root-blob")))

	cm := NewCacheManager(ctx, identity.NewID(), NewInMemoryCacheStorage(), &lazyResultStore{
		CacheResultStorage: NewInMemoryResultStorage(),
		lazy:               map[string]lazyOptKey{},
	})

	baseRes := testResult("base")
	cmSaveBase, err := cm.Save(NewCacheKey(dgst("base"), "", 0), baseRes, time.Now())
	require.NoError(t, err)

	midRes := testResult("mid")
	cmSaveMid, err := cm.Save(testCacheKey(dgst("mid"), 0, *cmSaveBase), midRes, time.Now())
	require.NoError(t, err)

	rootRes := testResult("root")
	cmSaveRoot, err := cm.Save(testCacheKey(dgst("root"), 0, *cmSaveMid), rootRes, time.Now())
	require.NoError(t, err)

	// The lazy blobs are only resolvable through the record-specific getters
	// (recordCtxOpts), except the root blob which the outer export context
	// (the caller-installed getter) knows too.
	cm.(*cacheManager).results.(*lazyResultStore).lazy = map[string]lazyOptKey{
		midRes.ID():  blobMid,
		rootRes.ID(): blobRoot,
	}

	// base record has no lazy blob and needs no getter.
	midExp := cmSaveMid.Exporter.(*exporter)
	rootExp := cmSaveRoot.Exporter.(*exporter)
	midExp.recordCtxOpts = cacheOptsCtx(map[lazyOptKey]any{blobMid: "mid-handler"})
	rootExp.recordCtxOpts = cacheOptsCtx(map[lazyOptKey]any{blobRoot: "root-handler"})

	// Caller-installed getter: knows the root (final result) chain only.
	outer := WithCacheOptGetter(ctx, outerGetter(map[lazyOptKey]any{blobRoot: "root-handler"}))

	target := &testCacheTarget{}
	_, err = rootExp.ExportTo(outer, target, testExportOpt())
	require.NoError(t, err)

	got := target.exportedBlobDigests()
	require.Contains(t, got, digest.Digest(blobMid),
		"intermediate (cross-stage) record was dropped from the export: its lazy blob was not resolvable")
	require.Contains(t, got, digest.Digest(blobRoot),
		"root record was dropped from the export")
}

// TestExportToKeepsCallerCacheOptsFallback verifies that a cache-opt getter
// installed by the caller of the export (e.g. withDescHandlerCacheOpts for
// the already-loaded final result ref) keeps working for records whose own
// record-specific getter cannot resolve the lazy blob (e.g. when the
// exporting record has no live solver state to walk).
func TestExportToKeepsCallerCacheOptsFallback(t *testing.T) {
	ctx := t.Context()

	blobRoot := lazyOptKey(digest.FromBytes([]byte("root-blob")))

	cm := NewCacheManager(ctx, identity.NewID(), NewInMemoryCacheStorage(), &lazyResultStore{
		CacheResultStorage: NewInMemoryResultStorage(),
		lazy:               map[string]lazyOptKey{},
	})
	rootRes := testResult("root")
	cmSaveRoot, err := cm.Save(NewCacheKey(dgst("root"), "", 0), rootRes, time.Now())
	require.NoError(t, err)
	cm.(*cacheManager).results.(*lazyResultStore).lazy = map[string]lazyOptKey{
		rootRes.ID(): blobRoot,
	}

	rootExp := cmSaveRoot.Exporter.(*exporter)
	// Record-specific getter that cannot resolve anything (no live state).
	rootExp.recordCtxOpts = cacheOptsCtx(map[lazyOptKey]any{})

	// Caller-installed getter knows the root blob.
	outer := WithCacheOptGetter(ctx, outerGetter(map[lazyOptKey]any{blobRoot: "root-handler"}))

	opt := testExportOpt()
	opt.ExportRoots = true

	target := &testCacheTarget{}
	_, err = rootExp.ExportTo(outer, target, opt)
	require.NoError(t, err)
	require.Contains(t, target.exportedBlobDigests(), digest.Digest(blobRoot),
		"caller-installed cache opts were not honored for the exported record")
}
