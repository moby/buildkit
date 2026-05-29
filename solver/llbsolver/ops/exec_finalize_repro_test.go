package ops

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/containerd/containerd/v2/core/diff/apply"
	ctdmetadata "github.com/containerd/containerd/v2/core/metadata"
	"github.com/containerd/containerd/v2/core/snapshots"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/containerd/v2/plugins/content/local"
	"github.com/containerd/containerd/v2/plugins/diff/walking"
	"github.com/containerd/containerd/v2/plugins/snapshots/native"
	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/cache/metadata"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/executor"
	resourcestypes "github.com/moby/buildkit/executor/resources/types"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/snapshot"
	containerdsnapshot "github.com/moby/buildkit/snapshot/containerd"
	"github.com/moby/buildkit/solver"
	"github.com/moby/buildkit/solver/llbsolver/errdefs"
	"github.com/moby/buildkit/solver/llbsolver/mounts"
	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/buildkit/util/leaseutil"
	"github.com/moby/buildkit/util/winlayers"
	"github.com/moby/buildkit/worker"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	bolt "go.etcd.io/bbolt"
)

// TestExecFailedOutputDoubleOwnerFinalizeRepro reproduces a snapshot-finalize
// failure of the form:
//
//		failed to finalize upper parent during diff: failed to commit <active> to <final> during finalize:
//		failed to stat active key during commit: snapshot <active> does not exist: not found
//
//	  - ExecOp.Exec, on a failed exec, commits each output mutable->immutable, appends
//	    the result to `results` (returned to the solver), AND the error-decoration defer
//	    stores that SAME *workerRefResult, UNCLONED, into ExecError.Mounts
//	    (solver/llbsolver/ops/exec.go, the `execMounts[...] = res` line). So a single
//	    committed output ref is returned through TWO independent ownership channels but
//	    is backed by only ONE counted cache ref.
//	  - The error-side owner is then released (modeling the gateway/solver error cleanup
//	    via the real ExecError.Release()). Because both channels share one refcount, that
//	    single release drains the record to zero, which cascades into equalMutable.release
//	    and deletes the mutable record's lease.
//	  - The active (mutable) snapshot is protected ONLY by that mutable lease (finalize
//	    never adds the active snapshot to a lease it controls before Snapshotter.Commit).
//	    Once the lease is gone, a real containerd metadata GC pass collects the active
//	    snapshot.
//	  - The surviving solver-side owner is then used as the upper parent of cm.Diff, whose
//	    createDiffRef calls upper.Finalize -> Snapshotter.Commit on the already-collected
//	    active snapshot -> the error above.
//
// At HEAD the two channels are the same pointer (the bug), so the single release frees
// what the surviving owner still needs. With the fix applied (exec.go cloning the result
// into ExecError.Mounts, plus workerRefResult.Clone giving the clone an independent
// *WorkerRef and counted cache ref), the error-side owner has its own ref; releasing it
// leaves the surviving owner's ref counted, the lease survives GC, and the Diff finalize
// succeeds.
//
// This is the genuinely buggy production path (exec.go's error decoration); applying the
// fix flips the outcome. No runc/root is required because PrepareMounts does not mount to
// disk (mounting happens inside the executor, which we stub to return a failure).
func TestExecFailedOutputDoubleOwnerFinalizeRepro(t *testing.T) {
	ctx := namespaces.WithNamespace(context.Background(), "buildkit-test")

	tmpdir := t.TempDir()
	co, err := newReproCacheManager(ctx, t, tmpdir)
	require.NoError(t, err)
	cm := co.manager

	// A finalized base immutable ref used as the exec input.
	inputBase := newFinalizedRef(ctx, t, cm)

	// An independent finalized ref used as the (non-ancestor) lower for cm.Diff so that
	// Diff takes the plain diff path and finalizes the upper parent at the wrapper site
	// "failed to finalize upper parent during diff".
	diffLower := newFinalizedRef(ctx, t, cm)

	w := stubWorker{}
	sm, err := session.NewManager()
	require.NoError(t, err)

	e := &ExecOp{
		op: &pb.ExecOp{
			Meta: &pb.Meta{
				Args: []string{"/bin/false"},
				Cwd:  "/",
			},
			Mounts: []*pb.Mount{
				{
					Dest:      pb.RootMount,
					Input:     0,
					Output:    0,
					MountType: pb.MountType_BIND,
				},
			},
		},
		cm:       cm,
		mm:       mounts.NewMountManager("test-exec-repro", cm, sm),
		sm:       sm,
		exec:     stubFailingExecutor{},
		w:        w,
		platform: &pb.Platform{OS: runtime.GOOS, Architecture: runtime.GOARCH},
	}

	inputRes := worker.NewWorkerRefResult(inputBase.Clone(), w)
	jobCtx := testJobContext(t)

	results, err := e.Exec(ctx, jobCtx, []solver.Result{inputRes})
	// The exec "failed", so we expect an error that carries an *ExecError.
	require.Error(t, err)
	require.Len(t, results, 1)

	var execErr *errdefs.ExecError
	require.True(t, errors.As(err, &execErr), "expected *errdefs.ExecError, got %T: %v", err, err)

	// Release the error-side ownership channel, exactly as the gateway/solver error
	// cleanup does. This is the ONLY release we perform; with the bug it drains the
	// single shared refcount and deletes the mutable lease.
	require.NoError(t, execErr.Release())

	// Run a real containerd metadata GC pass. With the mutable lease deleted, the active
	// snapshot is now unprotected and gets collected here. No manual snapshot deletion.
	require.NoError(t, co.gc(ctx))

	// The surviving solver-side owner. With the bug this is the same ref whose lease was
	// just deleted; with the fix it is an independent counted ref that kept its lease.
	upperWR, ok := results[0].Sys().(*worker.WorkerRef)
	require.True(t, ok)
	upper := upperWR.ImmutableRef

	diffRef, diffErr := cm.Diff(ctx, diffLower, upper, nil)

	// Correct behavior (with the ref-lifetime fixes applied): the error-side owner has
	// its own counted ref, so releasing it does not strip the surviving owner's lease,
	// the active snapshot survives GC, and the Diff finalizes cleanly.
	//
	// On unfixed code this fails with the error:
	//   failed to finalize upper parent during diff: failed to commit <active> to <final>
	//   during finalize: failed to stat active key during commit: snapshot <active> does
	//   not exist: not found
	require.NoErrorf(t, diffErr, "cm.Diff finalize failed because the failed-exec output "+
		"ref was released through one ownership channel while a second channel still owned it; "+
		"this is the snapshot-finalize regression")

	// Beyond not erroring, the fix must not LEAK refs. The exec.go call-site fix
	// (res.Clone() instead of res) is necessary, but on its own it is not sufficient
	// for *correct* ownership: if workerRefResult.Clone is still buggy it shares the
	// embedded *WorkerRef and reassigns ImmutableRef to a fresh clone, orphaning the
	// original cache ref. That orphan is an unreleasable holder which keeps the record
	// (and its lease) alive -- so the Diff above can pass for the WRONG reason (a leak)
	// rather than because ownership is correct.
	//
	// To catch that, release every ref this test owns and run a final GC. With both
	// fixes applied the cache drains completely; any retained record means a ref was
	// leaked.
	//
	// First capture a baseline: the surviving exec output must be in use right now, so
	// that the post-release "nothing in use" assertion is a genuine before/after
	// transition rather than a vacuous pass (e.g. if the output had already vanished).
	inUseIDs := func() map[string]bool {
		du, err := cm.DiskUsage(ctx, client.DiskUsageInfo{})
		require.NoError(t, err)
		m := map[string]bool{}
		for _, r := range du {
			if r.InUse {
				m[r.ID] = true
			}
		}
		return m
	}

	before := inUseIDs()
	require.Truef(t, before[upper.ID()], "expected surviving exec output %s to be in use "+
		"before release; in-use records were %v", upper.ID(), before)

	require.NoError(t, diffRef.Release(ctx))
	require.NoError(t, upper.Release(ctx))
	require.NoError(t, inputRes.Release(ctx))
	require.NoError(t, inputBase.Release(ctx))
	require.NoError(t, diffLower.Release(ctx))
	require.NoError(t, co.gc(ctx))

	var stillInUse []string
	for id := range inUseIDs() {
		stillInUse = append(stillInUse, id)
	}
	require.Emptyf(t, stillInUse, "cache records still in use after releasing every ref and "+
		"running GC: %v. A failed-exec output ref was leaked -- this happens when "+
		"workerRefResult.Clone shares the embedded *WorkerRef and orphans the original "+
		"cache ref, leaving an unreleasable holder (refs>0 forever)", stillInUse)
}

// stubWorker is a minimal worker.Worker used only so NewWorkerRefResult can stamp an ID
// onto the WorkerRef. None of the other worker methods are exercised by this test.
type stubWorker struct {
	worker.Worker
}

func (stubWorker) ID() string { return "stub-worker" }

// stubFailingExecutor models a process that exits non-zero. It does not touch the rootfs
// or mounts, so no runc/root is required; it only needs to return an error so ExecOp.Exec
// takes its failed-exec path.
type stubFailingExecutor struct{}

func (stubFailingExecutor) Run(ctx context.Context, id string, rootfs executor.Mount, mounts []executor.Mount, process executor.ProcessInfo, started chan<- struct{}) (resourcestypes.Recorder, error) {
	if started != nil {
		close(started)
	}
	return nil, errors.New("process exited with non-zero status")
}

func (stubFailingExecutor) Exec(ctx context.Context, id string, process executor.ProcessInfo) error {
	return errors.New("not implemented")
}

// newFinalizedRef creates a finalized immutable ref backed by a real committed snapshot.
func newFinalizedRef(ctx context.Context, t *testing.T, cm cache.Manager) cache.ImmutableRef {
	active, err := cm.New(ctx, nil, nil, cache.CachePolicyRetain)
	require.NoError(t, err)
	snap, err := active.Commit(ctx)
	require.NoError(t, err)
	require.NoError(t, snap.Finalize(ctx))
	return snap
}

type reproCMOut struct {
	manager cache.Manager
	gc      func(context.Context) error
}

func newReproCacheManager(ctx context.Context, t *testing.T, tmpdir string) (*reproCMOut, error) {
	ns, ok := namespaces.Namespace(ctx)
	if !ok {
		return nil, errors.Errorf("namespace required for test")
	}

	snapshotterName := "native"
	snapshotter, err := native.NewSnapshotter(filepath.Join(tmpdir, "snapshots"))
	if err != nil {
		return nil, err
	}

	store, err := local.NewStore(tmpdir)
	if err != nil {
		return nil, err
	}

	db, err := bolt.Open(filepath.Join(tmpdir, "containerdmeta.db"), 0644, nil)
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() {
		require.NoError(t, db.Close())
	})

	mdb := ctdmetadata.NewDB(db, store, map[string]snapshots.Snapshotter{
		snapshotterName: snapshotter,
	})
	if err := mdb.Init(context.TODO()); err != nil {
		return nil, err
	}

	lm := leaseutil.WithNamespace(ctdmetadata.NewLeaseManager(mdb), ns)
	c := mdb.ContentStore()
	applier := winlayers.NewFileSystemApplierWithWindows(c, apply.NewFileSystemApplier(c))
	differ := winlayers.NewWalkingDiffWithWindows(c, walking.NewWalkingDiff(c))

	md, err := metadata.NewStore(filepath.Join(tmpdir, "metadata.db"))
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() {
		require.NoError(t, md.Close())
	})

	cm, err := cache.NewManager(cache.ManagerOpt{
		Snapshotter:    snapshot.FromContainerdSnapshotter(snapshotterName, containerdsnapshot.NSSnapshotter(ns, mdb.Snapshotter(snapshotterName)), nil),
		MetadataStore:  md,
		ContentStore:   c,
		Applier:        applier,
		Differ:         differ,
		LeaseManager:   lm,
		GarbageCollect: mdb.GarbageCollect,
		Root:           tmpdir,
		MountPoolRoot:  filepath.Join(tmpdir, "cachemounts"),
	})
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() {
		require.NoError(t, cm.Close())
	})

	return &reproCMOut{
		manager: cm,
		gc: func(ctx context.Context) error {
			_, err := mdb.GarbageCollect(ctx)
			return err
		},
	}, nil
}
