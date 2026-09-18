package cache

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/containerd/v2/plugins/diff/walking"
	"github.com/containerd/containerd/v2/plugins/snapshots/native"
	"github.com/containerd/continuity/fs/fstest"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/buildkit/snapshot"
	"github.com/moby/buildkit/util/compression"
	"github.com/moby/buildkit/util/leaseutil"
	"github.com/moby/buildkit/util/winlayers"
	digest "github.com/opencontainers/go-digest"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
)

// lockedIngestStore wraps a content store and emulates the ingest locking of
// the containerd content store: a ref is locked when its writer is opened and
// only unlocked once the ingest is committed. containerd releases that lock
// asynchronously (the gRPC proxy's Close is a fire-and-forget CloseSend), so a
// writer abandoned by a failed attempt still holds its ref while the following
// attempt starts.
type lockedIngestStore struct {
	content.Store

	// failFirst makes the writes of the first opened writer fail, leaving its
	// ingest behind and locked.
	failFirst bool

	mu         sync.Mutex
	locked     map[string]struct{}
	refs       []string
	lockedNums []int // number of refs still locked when each writer was opened
}

func newLockedIngestStore(cs content.Store, failFirst bool) *lockedIngestStore {
	return &lockedIngestStore{
		Store:     cs,
		failFirst: failFirst,
		locked:    map[string]struct{}{},
	}
}

func (s *lockedIngestStore) Writer(ctx context.Context, opts ...content.WriterOpt) (content.Writer, error) {
	var wOpts content.WriterOpts
	for _, opt := range opts {
		if err := opt(&wOpts); err != nil {
			return nil, err
		}
	}

	s.mu.Lock()
	if _, ok := s.locked[wOpts.Ref]; ok {
		s.mu.Unlock()
		return nil, errors.Wrapf(cerrdefs.ErrUnavailable, "ref %s locked", wOpts.Ref)
	}
	s.lockedNums = append(s.lockedNums, len(s.locked))
	s.locked[wOpts.Ref] = struct{}{}
	s.refs = append(s.refs, wOpts.Ref)
	fail := s.failFirst && len(s.refs) == 1
	s.mu.Unlock()

	w, err := s.Store.Writer(ctx, opts...)
	if err != nil {
		return nil, err
	}
	return &lockedIngestWriter{Writer: w, store: s, ref: wOpts.Ref, fail: fail}, nil
}

func (s *lockedIngestStore) unlock(ref string) {
	s.mu.Lock()
	delete(s.locked, ref)
	s.mu.Unlock()
}

func (s *lockedIngestStore) ingestRefs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.refs)
}

func (s *lockedIngestStore) lockedOnOpen() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.lockedNums)
}

type lockedIngestWriter struct {
	content.Writer
	store *lockedIngestStore
	ref   string
	fail  bool
}

func (w *lockedIngestWriter) Write(p []byte) (int, error) {
	if w.fail {
		return 0, errors.New("simulated failure while writing the diff")
	}
	return w.Writer.Write(p)
}

func (w *lockedIngestWriter) Commit(ctx context.Context, size int64, expected digest.Digest, opts ...content.Opt) error {
	w.store.unlock(w.ref)
	return w.Writer.Commit(ctx, size, expected, opts...)
}

func (w *lockedIngestWriter) Close() error {
	// A writer that failed keeps its ref locked, emulating a fallback that
	// starts before containerd has released the ingest of the previous
	// attempt.
	if !w.fail {
		w.store.unlock(w.ref)
	}
	return w.Writer.Close()
}

// TestComputeBlobChainFallbackIngestRef makes sure that a fallback to the next
// differ doesn't reuse the ingest reference of the attempt that just failed:
// that ingest is still locked by the abandoned writer, so reusing the reference
// fails the whole computation with "ref ... locked: unavailable". Every attempt
// has to open its own ingest, derived from the cache record ID.
func TestComputeBlobChainFallbackIngestRef(t *testing.T) {
	t.Parallel()
	ctx := namespaces.WithNamespace(t.Context(), "buildkit-test")

	tmpdir := t.TempDir()

	snapshotter, err := native.NewSnapshotter(filepath.Join(tmpdir, "snapshots"))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, snapshotter.Close())
	})

	co, cleanup, err := newCacheManager(ctx, t, cmOpt{
		snapshotter:     snapshotter,
		snapshotterName: "native",
	})
	require.NoError(t, err)
	t.Cleanup(cleanup)

	ctx, done, err := leaseutil.WithLease(ctx, co.lm, leaseutil.MakeTemporary)
	require.NoError(t, err)
	defer done(t.Context())

	cs := newLockedIngestStore(co.cs, true)
	cm := co.manager.(*cacheManager)
	cm.ContentStore = cs
	cm.Differ = winlayers.NewWalkingDiffWithWindows(cs, walking.NewWalkingDiff(cs))

	active, err := cm.New(ctx, nil, nil)
	require.NoError(t, err)
	m, err := active.Mount(ctx, false, nil)
	require.NoError(t, err)
	lm := snapshot.LocalMounter(m)
	target, err := lm.Mount()
	require.NoError(t, err)
	err = fstest.Apply(
		fstest.CreateFile("foo", []byte("bar"), 0600),
	).Apply(target)
	require.NoError(t, err)
	require.NoError(t, lm.Unmount())
	snap, err := active.Commit(ctx)
	require.NoError(t, err)

	// A gzip level isn't supported by the containerd differ, so buildkit
	// computes the diff itself first and falls back to cm.Differ on failure.
	// The first attempt is made to fail while its ingest stays locked.
	err = snap.(*immutableRef).computeBlobChain(ctx, true, compression.New(compression.Gzip).SetLevel(5), nil)
	require.NoError(t, err)

	refs := cs.ingestRefs()
	require.Len(t, refs, 2)
	require.NotEqual(t, refs[0], refs[1])
	for _, ref := range refs {
		require.True(t, strings.HasPrefix(ref, snap.ID()), "ingest ref %q is not derived from the cache record", ref)
	}
	// the fallback started while the ingest of the failed attempt was still locked
	require.Equal(t, []int{0, 1}, cs.lockedOnOpen())

	require.NotEqual(t, digest.Digest(""), snap.(*immutableRef).getBlob())
}
