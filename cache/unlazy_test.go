package cache

import (
	"bytes"
	"context"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/diff"
	"github.com/containerd/containerd/v2/core/mount"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/util/contentutil"
	"github.com/moby/buildkit/util/progress"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
)

// TestUnlazySharedWithCanceledCaller pauses unlazy work of a lazy parent layer
// that is shared by two callers, cancels the first caller, runs GC and checks
// that the second caller still succeeds. The first caller is a child layer,
// whose unlazy holds a lease. "apply" pauses the parent's extraction before its
// snapshot is committed, "download" pauses the parent's blob download.
func TestUnlazySharedWithCanceledCaller(t *testing.T) {
	// windows fails when lazy blob is being extracted with "invalid windows mount type: 'bind'"
	if runtime.GOOS != "linux" {
		t.Skipf("unsupported GOOS: %s", runtime.GOOS)
	}
	t.Parallel()
	for _, tc := range []string{"apply", "download"} {
		t.Run(tc, func(t *testing.T) {
			t.Parallel()
			ctx := namespaces.WithNamespace(t.Context(), "buildkit-test")

			co, cleanup, err := newCacheManager(ctx, t, cmOpt{})
			require.NoError(t, err)
			t.Cleanup(cleanup)
			cm := co.manager.(*cacheManager)

			p := newPausePoint(t)
			parentBlob, parentDesc, err := mapToBlob(map[string]string{"parent": "parent"}, true)
			require.NoError(t, err)
			buf := contentutil.NewBuffer()
			require.NoError(t, content.WriteBlob(ctx, buf, "parent", bytes.NewReader(parentBlob), parentDesc))
			var provider content.Provider = buf
			if tc == "download" {
				provider = &pausedProvider{Provider: buf, pause: p}
			} else {
				cm.Applier = &pausedApplier{Applier: cm.Applier, pause: p}
			}
			dhs := DescHandlers{parentDesc.Digest: {
				Provider: func(session.Group) content.Provider { return provider },
			}}
			parent, err := cm.GetByBlob(ctx, parentDesc, nil, dhs)
			require.NoError(t, err)

			childBlob, childDesc, err := mapToBlob(map[string]string{"child": "child"}, true)
			require.NoError(t, err)
			require.NoError(t, content.WriteBlob(ctx, co.cs, "child", bytes.NewReader(childBlob), childDesc))
			child, err := cm.GetByBlob(ctx, childDesc, parent, dhs)
			require.NoError(t, err)

			ctx1, cancel1 := context.WithCancelCause(ctx)
			defer cancel1(errors.WithStack(context.Canceled))
			err1 := p.start(ctx1, t, func(ctx context.Context) error {
				return child.Extract(ctx, nil)
			})
			err2 := p.start(ctx, t, func(ctx context.Context) error {
				if tc == "download" {
					return lazyRefProvider{ref: parent.(*immutableRef), desc: parentDesc, dh: dhs[parentDesc.Digest]}.Unlazy(ctx)
				}
				return parent.Extract(ctx, nil)
			})

			cancel1(errors.WithStack(context.Canceled))
			require.ErrorIs(t, <-err1, context.Canceled)
			_, err = cm.GarbageCollect(ctx)
			require.NoError(t, err)
			p.release()
			require.NoError(t, <-err2)

			// The temporary leases are gone and the result is still kept by the parent.
			ls, err := co.lm.List(ctx, `labels."buildkit/lease.temporary"`)
			require.NoError(t, err)
			require.Empty(t, ls)
			_, err = cm.GarbageCollect(ctx)
			require.NoError(t, err)
			_, err = co.cs.Info(ctx, parentDesc.Digest)
			require.NoError(t, err)
			if tc == "apply" {
				_, err = cm.Snapshotter.Stat(ctx, parent.(*immutableRef).getSnapshotID())
				require.NoError(t, err)
			}
		})
	}
}

// pausePoint blocks shared unlazy work until released. On reaching it, the work
// writes a progress item. flightcontrol sends a call's progress to all of its
// callers, also to callers that join later, so a caller that sees the item is
// waiting on the paused call.
type pausePoint struct {
	ch   chan struct{}
	once sync.Once
}

func newPausePoint(t *testing.T) *pausePoint {
	p := &pausePoint{ch: make(chan struct{})}
	t.Cleanup(p.release)
	return p
}

func (p *pausePoint) wait(ctx context.Context) error {
	pw, _, _ := progress.NewFromContext(ctx)
	defer pw.Close()
	pw.Write("paused", progress.Status{Action: "paused"})
	select {
	case <-p.ch:
		return nil
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

func (p *pausePoint) release() {
	p.once.Do(func() { close(p.ch) })
}

// start runs f in the background and returns once f waits on the paused call.
func (p *pausePoint) start(ctx context.Context, t *testing.T, f func(context.Context) error) <-chan error {
	t.Helper()
	pr, ctx, closeProgress := progress.NewContext(ctx)
	t.Cleanup(func() { closeProgress(nil) })
	errCh := make(chan error, 1)
	go func() { errCh <- f(ctx) }()

	paused := make(chan struct{})
	go func() {
		for {
			ps, err := pr.Read(ctx)
			if err != nil {
				return
			}
			for _, item := range ps {
				if item.ID == "paused" {
					close(paused)
					return
				}
			}
		}
	}()
	select {
	case <-paused:
	case err := <-errCh:
		require.FailNow(t, "returned before reaching the pause", "%+v", err)
	case <-time.After(time.Minute):
		require.FailNow(t, "timed out waiting for the pause")
	}
	return errCh
}

// pausedProvider pauses the download with the blob's ingest already open.
type pausedProvider struct {
	content.Provider
	pause *pausePoint
}

func (p *pausedProvider) ReaderAt(ctx context.Context, desc ocispecs.Descriptor) (content.ReaderAt, error) {
	if err := p.pause.wait(ctx); err != nil {
		return nil, err
	}
	return p.Provider.ReaderAt(ctx, desc)
}

// pausedApplier pauses after the blob is applied, before unlazyLayer commits
// the extract snapshot.
type pausedApplier struct {
	diff.Applier
	pause *pausePoint
}

func (a *pausedApplier) Apply(ctx context.Context, desc ocispecs.Descriptor, mounts []mount.Mount, opts ...diff.ApplyOpt) (ocispecs.Descriptor, error) {
	applied, err := a.Applier.Apply(ctx, desc, mounts, opts...)
	if err != nil {
		return applied, err
	}
	return applied, a.pause.wait(ctx)
}
