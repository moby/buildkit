package worker

import (
	"testing"

	"github.com/moby/buildkit/cache"
	"github.com/stretchr/testify/require"
)

// stubImmutableRef is a minimal cache.ImmutableRef stub for testing
// workerRefResult ownership semantics. Only ID and Clone are used by these
// tests; any other method call will panic via the embedded nil interface,
// which is intentional so accidental usage is loud.
type stubImmutableRef struct {
	cache.ImmutableRef
	id     string
	parent *stubImmutableRef
}

func (r *stubImmutableRef) ID() string {
	return r.id
}

func (r *stubImmutableRef) Clone() cache.ImmutableRef {
	return &stubImmutableRef{id: r.id, parent: r}
}

// TestWorkerRefResultCloneOwnership pins the ownership invariant that
// workerRefResult.Clone must produce a result with an independent *WorkerRef.
//
// This is the structural pre-condition behind the "failed to finalize upper
// parent during diff: ... snapshot does not exist" snapshot-finalization
// failure: when multiple owners share the same *WorkerRef they
// also share the same cache.ImmutableRef, so independent Release/Finalize
// paths in the gateway forwarder and ExecError decoration end up acting on
// the same underlying refcount instead of independent clones.
//
// The buggy implementation does:
//
//	r2 := *r
//	if r.ImmutableRef != nil {
//	    r.ImmutableRef = r.ImmutableRef.Clone()
//	}
//	return &r2
//
// Because workerRefResult embeds *WorkerRef (a pointer), `r2 := *r` copies the
// pointer, not the WorkerRef struct, so both `r` and `r2` continue to point at
// the same WorkerRef and the subsequent `r.ImmutableRef = ...` mutates the
// shared WorkerRef. The fix copies the WorkerRef struct first and assigns the
// clone to the copy, so the original WorkerRef is untouched.
func TestWorkerRefResultCloneOwnership(t *testing.T) {
	orig := &stubImmutableRef{id: "orig"}
	res := &workerRefResult{&WorkerRef{ImmutableRef: orig}}

	cloned := res.Clone().(*workerRefResult)

	require.NotSame(t, res.WorkerRef, cloned.WorkerRef,
		"workerRefResult.Clone must produce an independent *WorkerRef so each result owns its own cache ref")
	require.Same(t, orig, res.ImmutableRef,
		"workerRefResult.Clone must not mutate the original WorkerRef.ImmutableRef")
	require.NotNil(t, cloned.ImmutableRef,
		"cloned workerRefResult must hold a non-nil ImmutableRef")
	require.NotSame(t, orig, cloned.ImmutableRef,
		"cloned workerRefResult must hold a freshly cloned ImmutableRef, not the original")

	// Sys() must return the WorkerRef that backs the receiver; this is what
	// the gateway forwarder reads to populate workerRefByID and what the
	// exec error decoration walks to find result refs. If Sys() crossed
	// owners, callers would silently share ownership.
	require.Same(t, res.WorkerRef, res.Sys().(*WorkerRef),
		"workerRefResult.Sys must return the result's own WorkerRef")
	require.Same(t, cloned.WorkerRef, cloned.Sys().(*WorkerRef),
		"cloned workerRefResult.Sys must return the clone's own WorkerRef")
}

// TestWorkerRefResultCloneNilRef ensures Clone is safe when there is no
// ImmutableRef set, which the production code relies on via the early nil
// check before invoking ImmutableRef.Clone.
func TestWorkerRefResultCloneNilRef(t *testing.T) {
	res := &workerRefResult{&WorkerRef{}}

	cloned := res.Clone().(*workerRefResult)

	require.NotSame(t, res.WorkerRef, cloned.WorkerRef,
		"workerRefResult.Clone must produce an independent *WorkerRef even when ImmutableRef is nil")
	require.Nil(t, res.ImmutableRef)
	require.Nil(t, cloned.ImmutableRef)
}
