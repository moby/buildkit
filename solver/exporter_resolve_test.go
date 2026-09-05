package solver

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/util/compression"
	digest "github.com/opencontainers/go-digest"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

// A cache export walks every record in the graph. For each one it first asks the
// result storage for a remote, which is cheap, and then may load the result and
// resolve it again, which is not: loading a result that came from an imported
// cache rebuilds its whole ref chain a blob at a time under a global lock.
//
// The tests below pin down when that second step happens.

// countingResultStore hands out a remote for every result, the way a storage
// backed by an imported cache manifest does, and counts what the exporter asks
// of it.
type countingResultStore struct {
	CacheResultStorage

	// mediaTypes is cycled through when building a remote's descriptors. One
	// entry gives a uniform chain, several give a mixed one.
	mediaTypes []string
	noRemotes  bool

	// perLayerLoad stands in for the work FromRemote does when it materializes
	// a result: one GetByBlob per layer, each taking the cache manager's global
	// lock. Cost therefore grows with the record's chain depth, which is what
	// makes the total quadratic over a chain.
	perLayerLoad time.Duration

	mu      sync.Mutex
	remotes []*Remote      // by step, so step i has i+1 layers
	steps   map[string]int // result ID to step

	loads        atomic.Int64
	loadRemotes  atomic.Int64
	loadedLayers atomic.Int64
	resolves     atomic.Int64
}

func newCountingResultStore(mediaTypes ...string) *countingResultStore {
	return &countingResultStore{
		CacheResultStorage: NewInMemoryResultStorage(),
		mediaTypes:         mediaTypes,
		steps:              map[string]int{},
	}
}

// addResult registers the result of the next step of the chain. Its remote has
// one layer per step so far, the way a Dockerfile step's does; a fixed layer
// count would hide the quadratic that the benchmarks measure.
func (s *countingResultStore) addResult(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	step := len(s.remotes)
	r := &Remote{}
	for i := range step + 1 {
		r.Descriptors = append(r.Descriptors, ocispecs.Descriptor{
			Digest:    digest.FromString(fmt.Sprintf("%s-layer-%d", id, i)),
			MediaType: s.mediaTypes[i%len(s.mediaTypes)],
			Size:      int64(i + 1),
		})
	}
	s.remotes = append(s.remotes, r)
	s.steps[id] = step
}

func (s *countingResultStore) remote(id string) *Remote {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.remotes[s.steps[id]]
}

func (s *countingResultStore) remoteForStep(step int) *Remote {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.remotes[step]
}

func (s *countingResultStore) Load(ctx context.Context, res CacheResult) (Result, error) {
	s.loads.Add(1)
	n := len(s.remote(res.ID).Descriptors)
	s.loadedLayers.Add(int64(n))
	if s.perLayerLoad > 0 {
		time.Sleep(time.Duration(n) * s.perLayerLoad)
	}
	return s.CacheResultStorage.Load(ctx, res)
}

func (s *countingResultStore) LoadRemotes(_ context.Context, res CacheResult, _ *compression.Config, _ session.Group) ([]*Remote, error) {
	s.loadRemotes.Add(1)
	if s.noRemotes {
		return nil, nil
	}
	return []*Remote{s.remote(res.ID)}, nil
}

// resolveRemotes is the CacheExportOpt.ResolveRemotes counterpart: it returns
// the same remote the store would, and counts the call.
func (s *countingResultStore) resolveRemotes(_ context.Context, r Result) ([]*Remote, error) {
	s.resolves.Add(1)
	return []*Remote{s.remote(r.ID())}, nil
}

type testExportRecord struct {
	CacheExporterRecordBase
	results []CacheExportResult
}

type testExportTarget struct {
	mu      sync.Mutex
	records map[digest.Digest]*testExportRecord
}

func newTestExportTarget() *testExportTarget {
	return &testExportTarget{records: map[digest.Digest]*testExportRecord{}}
}

func (t *testExportTarget) Add(dgst digest.Digest, _ [][]CacheLink, results []CacheExportResult) (CacheExporterRecord, bool, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	r, ok := t.records[dgst]
	if !ok {
		r = &testExportRecord{}
		t.records[dgst] = r
	}
	r.results = append(r.results, results...)
	return r, true, nil
}

// resultsFor returns what was recorded for the given step of a chain built by
// buildChain.
func (t *testExportTarget) resultsFor(step int) []CacheExportResult {
	t.mu.Lock()
	defer t.mu.Unlock()
	if r, ok := t.records[rootKey(stepDigest(step), 0)]; ok {
		return r.results
	}
	return nil
}

func stepDigest(step int) digest.Digest {
	return dgst(fmt.Sprintf("step-%d", step))
}

// buildChain saves a linear chain of depth records into a cache manager backed
// by store and returns the topmost key. This is the shape a Dockerfile
// produces: every step depends on the one before it.
//
// Step 0 has no dependencies, and none of the exports below set ExportRoots,
// so an export of the chain visits depth-1 records.
func buildChain(t testing.TB, store *countingResultStore, depth int) *ExportableCacheKey {
	t.Helper()
	cm := NewCacheManager(t.Context(), "test-cache-manager", NewInMemoryCacheStorage(), store)
	now := time.Now()

	var key *ExportableCacheKey
	for step := range depth {
		k := NewCacheKey(stepDigest(step), "", 0)
		if key != nil {
			k = testCacheKey(stepDigest(step), 0, *key)
		}
		r := testResult(fmt.Sprintf("res-%d", step))
		store.addResult(r.ID())

		var err error
		key, err = cm.Save(k, r, now)
		require.NoError(t, err)
	}
	return key
}

// exportChain runs a cache export over a chain of the given depth and returns
// the target the records were added to.
func exportChain(t testing.TB, store *countingResultStore, depth int, comp *compression.Config, mode CacheExportMode) *testExportTarget {
	t.Helper()

	key := buildChain(t, store, depth)
	target := newTestExportTarget()

	_, err := key.Exporter.ExportTo(t.Context(), target, CacheExportOpt{
		ResolveRemotes: store.resolveRemotes,
		Mode:           mode,
		CompressionOpt: comp,
	})
	require.NoError(t, err)
	return target
}

// requireLoads asserts how many results the export loaded and resolved, which
// is the expensive step the tests are about.
func requireLoads(t *testing.T, store *countingResultStore, want int) {
	t.Helper()
	require.EqualValues(t, want, store.loads.Load(), "results loaded")
	require.EqualValues(t, want, store.resolves.Load(), "results resolved")
}

// TestExportSkipsResolveWhenCompressionAlreadyMatches is the point of the
// change. When the remote we already have is entirely in the requested
// compression, loading and resolving the result cannot produce anything better,
// so it must not happen, and the remote we had is what gets recorded.
func TestExportSkipsResolveWhenCompressionAlreadyMatches(t *testing.T) {
	const depth = 20
	gzip := compression.New(compression.Gzip)
	store := newCountingResultStore(ocispecs.MediaTypeImageLayerGzip)

	target := exportChain(t, store, depth, &gzip, CacheExportModeMax)

	require.EqualValues(t, depth-1, store.loadRemotes.Load(), "remotes must still be read for every record")
	requireLoads(t, store, 0)

	for step := 1; step < depth; step++ {
		results := target.resultsFor(step)
		require.Len(t, results, 1, "step %d", step)
		require.Equal(t, store.remoteForStep(step).Descriptors, results[0].Result.Descriptors, "step %d", step)
	}
}

// TestExportResolvesWhenCompressionDiffers checks the case the skip must not
// swallow. A remote in the wrong compression is exactly when resolving is worth
// its cost, because it can produce one in the right compression.
func TestExportResolvesWhenCompressionDiffers(t *testing.T) {
	const depth = 20
	zstd := compression.New(compression.Zstd)
	store := newCountingResultStore(ocispecs.MediaTypeImageLayerGzip)

	exportChain(t, store, depth, &zstd, CacheExportModeMax)

	requireLoads(t, store, depth-1)
}

// TestExportResolvesWhenNoRemote checks the other reason the load exists: with
// no remote at all, the result has to be loaded to produce one.
func TestExportResolvesWhenNoRemote(t *testing.T) {
	const depth = 20
	gzip := compression.New(compression.Gzip)
	store := newCountingResultStore(ocispecs.MediaTypeImageLayerGzip)
	store.noRemotes = true

	target := exportChain(t, store, depth, &gzip, CacheExportModeMax)

	requireLoads(t, store, depth-1)
	for step := 1; step < depth; step++ {
		require.Len(t, target.resultsFor(step), 1, "the resolved remote must be recorded for step %d", step)
	}
}

// TestExportPartialCompressionMatchStillResolves checks that the match is all
// or nothing. A chain where only some layers are in the requested compression
// keeps the existing behaviour and resolves.
func TestExportPartialCompressionMatchStillResolves(t *testing.T) {
	const depth = 20
	gzip := compression.New(compression.Gzip)
	store := newCountingResultStore(ocispecs.MediaTypeImageLayerGzip, ocispecs.MediaTypeImageLayerZstd)

	exportChain(t, store, depth, &gzip, CacheExportModeMax)

	requireLoads(t, store, depth-1)
}

// TestExportEStargzStillResolves is the case that makes the resolve genuinely
// necessary rather than merely defensive. An eStargz layer reports the plain
// gzip media type, because it is a gzip layer with a table of contents appended.
// So a chain of gzip descriptors is not evidence that the layers are eStargz,
// and asking for eStargz has to resolve even though every media type "matches".
func TestExportEStargzStillResolves(t *testing.T) {
	const depth = 20
	estargz := compression.New(compression.EStargz)
	store := newCountingResultStore(ocispecs.MediaTypeImageLayerGzip)

	exportChain(t, store, depth, &estargz, CacheExportModeMax)

	requireLoads(t, store, depth-1)
}

// TestExportWithoutCompressionOptResolvesOnlyWithoutRemote guards the path that
// does not ask for a compression at all. There the remote decides on its own,
// exactly as before.
func TestExportWithoutCompressionOptResolvesOnlyWithoutRemote(t *testing.T) {
	const depth = 20

	store := newCountingResultStore(ocispecs.MediaTypeImageLayerGzip)
	exportChain(t, store, depth, nil, CacheExportModeMax)
	requireLoads(t, store, 0)

	store = newCountingResultStore(ocispecs.MediaTypeImageLayerGzip)
	store.noRemotes = true
	exportChain(t, store, depth, nil, CacheExportModeMax)
	requireLoads(t, store, depth-1)
}

// TestExportMinModeResolvesOnlyTheFrontier guards the mode that was already
// fast. Once min mode has a remote it walks the rest of the chain remote-only,
// so at most the first record can pay for a resolve: none when its remote
// already matches, one when it does not.
func TestExportMinModeResolvesOnlyTheFrontier(t *testing.T) {
	const depth = 20
	gzip := compression.New(compression.Gzip)
	zstd := compression.New(compression.Zstd)

	store := newCountingResultStore(ocispecs.MediaTypeImageLayerGzip)
	target := exportChain(t, store, depth, &gzip, CacheExportModeMin)
	require.EqualValues(t, depth-1, store.loadRemotes.Load())
	requireLoads(t, store, 0)
	for step := 1; step < depth; step++ {
		require.Len(t, target.resultsFor(step), 1, "step %d", step)
	}

	store = newCountingResultStore(ocispecs.MediaTypeImageLayerGzip)
	exportChain(t, store, depth, &zstd, CacheExportModeMin)
	require.EqualValues(t, depth-1, store.loadRemotes.Load())
	requireLoads(t, store, 1)
}

// TestRemoteMatchesCompression covers the helper on its own.
func TestRemoteMatchesCompression(t *testing.T) {
	remoteOf := func(mediaTypes ...string) *Remote {
		r := &Remote{}
		for i, mt := range mediaTypes {
			r.Descriptors = append(r.Descriptors, ocispecs.Descriptor{
				Digest:    dgst(fmt.Sprintf("layer-%d", i)),
				MediaType: mt,
			})
		}
		return r
	}
	gzip := compression.New(compression.Gzip)

	for _, tc := range []struct {
		name   string
		remote *Remote
		comp   compression.Config
		want   bool
	}{
		{name: "nil remote", remote: nil, comp: gzip, want: false},
		{name: "empty remote", remote: &Remote{}, comp: gzip, want: false},
		{name: "all layers match", remote: remoteOf(ocispecs.MediaTypeImageLayerGzip, ocispecs.MediaTypeImageLayerGzip), comp: gzip, want: true},
		{name: "some layers match", remote: remoteOf(ocispecs.MediaTypeImageLayerGzip, ocispecs.MediaTypeImageLayerZstd), comp: gzip, want: false},
		{name: "no layer matches", remote: remoteOf(ocispecs.MediaTypeImageLayerZstd), comp: gzip, want: false},
		{name: "uncompressed", remote: remoteOf(ocispecs.MediaTypeImageLayer), comp: compression.New(compression.Uncompressed), want: true},
		{name: "unknown media type", remote: remoteOf("application/octet-stream"), comp: gzip, want: false},
		// eStargz shares the gzip media type, so a gzip descriptor proves nothing.
		{name: "estargz never matches", remote: remoteOf(ocispecs.MediaTypeImageLayerGzip), comp: compression.New(compression.EStargz), want: false},
		{name: "unset type", remote: remoteOf(ocispecs.MediaTypeImageLayerGzip), comp: compression.Config{}, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, remoteMatchesCompression(tc.remote, tc.comp))
		})
	}
}

func benchmarkExportChain(b *testing.B, comp compression.Config) {
	for _, depth := range []int{50, 100, 200} {
		b.Run(fmt.Sprintf("depth=%d", depth), func(b *testing.B) {
			// The export does not mutate the chain, so build it once and only
			// time the walk.
			store := newCountingResultStore(ocispecs.MediaTypeImageLayerGzip)
			store.perLayerLoad = 20 * time.Microsecond
			key := buildChain(b, store, depth)

			for b.Loop() {
				_, err := key.Exporter.ExportTo(b.Context(), newTestExportTarget(), CacheExportOpt{
					ResolveRemotes: store.resolveRemotes,
					Mode:           CacheExportModeMax,
					CompressionOpt: &comp,
				})
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkExportChainWarmRemotes measures the export walk over a chain whose
// remotes are already in the requested compression, which is the common shape
// for a mode=max build against a warm cache.
func BenchmarkExportChainWarmRemotes(b *testing.B) {
	benchmarkExportChain(b, compression.New(compression.Gzip))
}

// BenchmarkExportChainMismatchedRemotes is the control. Here the remotes are in
// the wrong compression, so every record still has to be resolved and the
// change must not move it.
func BenchmarkExportChainMismatchedRemotes(b *testing.B) {
	benchmarkExportChain(b, compression.New(compression.Zstd))
}
