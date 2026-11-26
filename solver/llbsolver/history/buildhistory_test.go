package history

import (
	"bytes"
	"context"
	"encoding/binary"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"github.com/containerd/containerd/v2/core/leases"
	ctdmetadata "github.com/containerd/containerd/v2/core/metadata"
	"github.com/containerd/containerd/v2/defaults"
	"github.com/containerd/containerd/v2/plugins/content/local"
	controlapi "github.com/moby/buildkit/api/services/control"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/cmd/buildkitd/config"
	containerdsnapshot "github.com/moby/buildkit/snapshot/containerd"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/leaseutil"
	digest "github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
	bolt "go.etcd.io/bbolt"
	"google.golang.org/protobuf/proto"
)

func TestQueueHistoryConfig(t *testing.T) {
	zero := int64(0)
	configured := int64(12)
	tests := []struct {
		name        string
		q           *Queue
		wantEnabled bool
		wantEntries int64
	}{
		{name: "default config", q: &Queue{}, wantEnabled: true, wantEntries: 50},
		{name: "age only", q: &Queue{opt: QueueOpt{CleanConfig: &config.HistoryConfig{}}}, wantEnabled: true},
		{name: "disabled", q: &Queue{opt: QueueOpt{CleanConfig: &config.HistoryConfig{MaxEntries: &zero}}}},
		{name: "configured", q: &Queue{opt: QueueOpt{CleanConfig: &config.HistoryConfig{MaxEntries: &configured}}}, wantEnabled: true, wantEntries: 12},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.wantEnabled, tc.q.Enabled())
			require.Equal(t, tc.wantEntries, tc.q.maxEntries())
		})
	}
}

func TestStatusSummary(t *testing.T) {
	completed := time.Now()
	first := digest.FromString("first")
	second := digest.FromString("second")
	var summary StatusSummary

	summary.Update(&client.SolveStatus{
		Vertexes: []*client.Vertex{
			{Digest: first, Cached: true},
			{Digest: second, Completed: &completed},
		},
		Warnings: []*client.VertexWarning{{}},
	})
	summary.Update(&client.SolveStatus{
		Vertexes: []*client.Vertex{
			{Digest: first, Cached: true, Completed: &completed},
		},
	})

	require.Equal(t, 1, summary.NumCachedSteps)
	require.Equal(t, 2, summary.NumCompletedSteps)
	require.Equal(t, 2, summary.NumTotalSteps)
	require.Equal(t, 1, summary.NumWarnings)
}

func TestOversizedStatusHistory(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		name := "import"
		if legacy {
			name = "legacy record"
		}
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			store, err := local.NewStore(filepath.Join(t.TempDir(), "content"))
			require.NoError(t, err)
			db, err := bolt.Open(filepath.Join(t.TempDir(), "metadata.db"), 0600, nil)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, db.Close()) })
			mdb := ctdmetadata.NewDB(db, store, nil)
			require.NoError(t, mdb.Init(ctx))
			// Exercise persistence and replay without starting the queue's GC loop.
			h := &Queue{
				opt:           QueueOpt{DB: db},
				hContentStore: containerdsnapshot.NewContentStore(mdb.ContentStore(), "test_history"),
				hLeaseManager: leaseutil.WithNamespace(ctdmetadata.NewLeaseManager(mdb), "test_history"),
			}
			require.NoError(t, h.init())
			original := &controlapi.StatusResponse{
				Logs: []*controlapi.VertexLog{{Msg: bytes.Repeat([]byte("log\n"), 5*1024*1024)}},
			}
			// Dockerfile warnings each carry a copy of the source file.
			source := &pb.SourceInfo{Filename: "Dockerfile", Data: bytes.Repeat([]byte("# source\n"), 64*1024)}
			for range 32 {
				original.Warnings = append(original.Warnings, &controlapi.VertexWarning{Short: []byte("warning"), Info: source})
			}
			var descriptor *controlapi.Descriptor
			if legacy {
				// Older builders could persist one record larger than the RPC limit.
				encoded, err := original.MarshalVT()
				require.NoError(t, err)
				require.Greater(t, len(encoded), defaults.DefaultMaxSendMsgSize)
				w, err := h.OpenBlobWriter(ctx, "application/vnd.buildkit.status.v0")
				require.NoError(t, err)
				var header [4]byte
				binary.LittleEndian.PutUint32(header[:], uint32(len(encoded)))
				_, err = w.Write(header[:])
				require.NoError(t, err)
				_, err = w.Write(encoded)
				require.NoError(t, err)
				desc, release, err := w.Commit(ctx)
				require.NoError(t, err)
				t.Cleanup(release)
				descriptor = &controlapi.Descriptor{Digest: string(desc.Digest), Size: desc.Size, MediaType: desc.MediaType}
			} else {
				input := make(chan *client.SolveStatus, 1)
				input <- client.NewSolveStatus(original)
				close(input)
				result, release, err := h.ImportStatus(ctx, input)
				require.NoError(t, err)
				t.Cleanup(release)
				require.Equal(t, len(original.Warnings), result.NumWarnings)
				descriptor = &controlapi.Descriptor{Digest: string(result.Descriptor.Digest), Size: result.Descriptor.Size, MediaType: result.Descriptor.MediaType}
			}
			record, err := (&controlapi.BuildHistoryRecord{Ref: "oversized", Logs: descriptor}).MarshalVT()
			require.NoError(t, err)
			require.NoError(t, db.Update(func(tx *bolt.Tx) error {
				return tx.Bucket([]byte(recordsBucket)).Put([]byte("oversized"), record)
			}))
			updates := make(chan *client.SolveStatus)
			replayErr := make(chan error, 1)
			go func() {
				defer close(updates)
				replayErr <- h.Status(ctx, "oversized", updates)
			}()
			var received []byte
			var warnings []*controlapi.VertexWarning
			maxSize := 0
			for update := range updates {
				// Controller.Status marshals each replayed update before sending it.
				for _, response := range update.Marshal() {
					maxSize = max(maxSize, proto.Size(response))
					for _, log := range response.Logs {
						received = append(received, log.Msg...)
					}
					warnings = append(warnings, response.Warnings...)
				}
			}
			require.NoError(t, <-replayErr)
			require.LessOrEqual(t, maxSize, 1024*1024)
			require.Equal(t, original.Logs[0].Msg, received)
			require.Len(t, warnings, len(original.Warnings))
			for i, warning := range warnings {
				require.True(t, proto.Equal(original.Warnings[i], warning))
			}
		})
	}
}

func newTestHistoryQueue(t *testing.T) *Queue {
	t.Helper()
	openDB := func(name string) *bolt.DB {
		db, err := bolt.Open(filepath.Join(t.TempDir(), name), 0600, nil)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, db.Close()) })
		return db
	}
	store, err := local.NewStore(t.TempDir())
	require.NoError(t, err)
	mdb := ctdmetadata.NewDB(openDB("metadata.db"), store, nil)
	require.NoError(t, mdb.Init(t.Context()))
	return &Queue{
		opt:           QueueOpt{DB: openDB("history.db"), GarbageCollect: func(context.Context) error { return nil }},
		hLeaseManager: leaseutil.WithNamespace(ctdmetadata.NewLeaseManager(mdb), "test_history"),
		ps:            &pubsub[*controlapi.BuildHistoryEvent]{m: map[*channel[*controlapi.BuildHistoryEvent]]struct{}{}},
		active:        map[string]*controlapi.BuildHistoryRecord{},
		refs:          map[string]int{},
		deleted:       map[string]struct{}{},
	}
}

func TestDeletedActiveBuildDoesNotReappear(t *testing.T) {
	h := newTestHistoryQueue(t)
	ctx := t.Context()
	record := &controlapi.BuildHistoryRecord{Ref: "active"}
	require.NoError(t, h.Update(ctx, &controlapi.BuildHistoryEvent{Type: controlapi.BuildHistoryEventType_STARTED, Record: record}))
	sub := h.ps.Subscribe()
	defer sub.close()
	require.NoError(t, h.Listen(ctx, &controlapi.BuildHistoryRequest{Ref: record.Ref, EarlyExit: true}, func(*controlapi.BuildHistoryEvent) error {
		return h.Delete(ctx, record.Ref)
	}))
	require.Contains(t, h.deleted, record.Ref)
	require.Equal(t, controlapi.BuildHistoryEventType_DELETED, (<-sub.ch).Type)
	require.NoError(t, h.Update(ctx, &controlapi.BuildHistoryEvent{Type: controlapi.BuildHistoryEventType_COMPLETE, Record: record}))
	require.Empty(t, sub.ch)
	require.NoError(t, h.Listen(ctx, &controlapi.BuildHistoryRequest{EarlyExit: true}, func(*controlapi.BuildHistoryEvent) error {
		t.Fatal("deleted build reappeared on completion")
		return nil
	}))
	require.Empty(t, h.deleted)
}

func TestDeleteWithHistoryReader(t *testing.T) {
	h := newTestHistoryQueue(t)
	const ref = "build"
	require.NoError(t, h.Update(t.Context(), &controlapi.BuildHistoryEvent{
		Type:   controlapi.BuildHistoryEventType_COMPLETE,
		Record: &controlapi.BuildHistoryRecord{Ref: ref},
	}))

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancelCause(t.Context())
		defer cancel(nil)
		var events []controlapi.BuildHistoryEventType
		done := make(chan error, 1)
		go func() {
			done <- h.Listen(ctx, &controlapi.BuildHistoryRequest{}, func(e *controlapi.BuildHistoryEvent) error {
				events = append(events, e.Type)
				return nil
			})
		}()
		synctest.Wait()
		require.Equal(t, []controlapi.BuildHistoryEventType{controlapi.BuildHistoryEventType_COMPLETE}, events)

		require.NoError(t, h.Listen(ctx, &controlapi.BuildHistoryRequest{Ref: ref, EarlyExit: true}, func(*controlapi.BuildHistoryEvent) error {
			require.NoError(t, h.Delete(ctx, ref))
			require.NoError(t, h.Delete(ctx, ref))
			require.NoError(t, h.UpdateRef(ctx, ref, func(r *controlapi.BuildHistoryRecord) error {
				r.Pinned = true
				return nil
			}))
			synctest.Wait()
			require.Equal(t, []controlapi.BuildHistoryEventType{
				controlapi.BuildHistoryEventType_COMPLETE,
				controlapi.BuildHistoryEventType_DELETED,
			}, events)
			require.NoError(t, h.Listen(ctx, &controlapi.BuildHistoryRequest{EarlyExit: true}, func(*controlapi.BuildHistoryEvent) error {
				t.Fatal("deleted record appeared in a fresh history listing")
				return nil
			}))
			// Deferred deletion must still remove the record if its lease is gone.
			return h.hLeaseManager.Delete(ctx, leases.Lease{ID: h.leaseID(ref)})
		}))
		synctest.Wait()
		require.Len(t, events, 2)
		require.Empty(t, h.refs)
		require.Empty(t, h.deleted)
		require.NoError(t, h.opt.DB.View(func(tx *bolt.Tx) error {
			require.Nil(t, tx.Bucket([]byte(recordsBucket)).Get([]byte(ref)))
			return nil
		}))
		cancel(nil)
		require.ErrorIs(t, <-done, context.Canceled)
	})
}

func TestDeleteAfterHistoryReaderExits(t *testing.T) {
	h := newTestHistoryQueue(t)
	ctx := t.Context()
	const ref = "build"
	require.NoError(t, h.Update(ctx, &controlapi.BuildHistoryEvent{
		Type:   controlapi.BuildHistoryEventType_COMPLETE,
		Record: &controlapi.BuildHistoryRecord{Ref: ref},
	}))
	require.NoError(t, h.Listen(ctx, &controlapi.BuildHistoryRequest{Ref: ref, EarlyExit: true}, func(*controlapi.BuildHistoryEvent) error {
		return nil
	}))
	sub := h.ps.Subscribe()
	defer sub.close()
	require.NoError(t, h.Delete(ctx, ref))
	require.Empty(t, h.deleted)
	require.NoError(t, h.opt.DB.View(func(tx *bolt.Tx) error {
		require.Nil(t, tx.Bucket([]byte(recordsBucket)).Get([]byte(ref)))
		return nil
	}))
	require.Error(t, h.Delete(ctx, ref))
	require.Len(t, sub.ch, 1)
	require.Equal(t, controlapi.BuildHistoryEventType_DELETED, (<-sub.ch).Type)
}
