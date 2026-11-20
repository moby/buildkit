package cache

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/containerd/containerd/v2/core/leases"
	"github.com/containerd/containerd/v2/pkg/labels"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/stargz-snapshotter/estargz"
	digest "github.com/opencontainers/go-digest"
	imagespecidentity "github.com/opencontainers/image-spec/identity"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

func TestGetByBlobAllowsParentWithoutCompressedBlob(t *testing.T) {
	ctx := namespaces.WithNamespace(t.Context(), "buildkit-test")
	co, cleanup, err := newCacheManager(ctx, t, cmOpt{})
	require.NoError(t, err)
	t.Cleanup(cleanup)
	parent, err := co.manager.GetByBlob(ctx, ocispecs.Descriptor{
		Annotations: map[string]string{labels.LabelUncompressed: digest.FromString("moby base").String()},
	}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, parent.Release(context.WithoutCancel(ctx))) })
	child, err := co.manager.GetByBlob(ctx, ocispecs.Descriptor{
		Annotations: map[string]string{labels.LabelUncompressed: digest.FromString("moby child").String()},
	}, parent)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, child.Release(context.WithoutCancel(ctx))) })
}

func TestBlobReusingDiffChainMetadata(t *testing.T) {
	for _, snapshotter := range []string{"native", "stargz"} {
		t.Run(snapshotter, func(t *testing.T) {
			ctx := namespaces.WithNamespace(t.Context(), "buildkit-test")
			co, cleanup, err := newCacheManager(ctx, t, cmOpt{snapshotterName: snapshotter})
			require.NoError(t, err)
			t.Cleanup(cleanup)
			lease, err := co.lm.Create(ctx, func(l *leases.Lease) error {
				l.ID = "chain-metadata-test"
				return nil
			})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, co.lm.Delete(context.WithoutCancel(ctx), lease)) })
			ctx = leases.WithLease(ctx, lease.ID)
			baseDesc := ocispecs.Descriptor{
				Digest:      digest.FromString("base blob"),
				Annotations: map[string]string{labels.LabelUncompressed: digest.FromString("base diff").String()},
			}
			upperDesc := ocispecs.Descriptor{
				Digest:      digest.FromString("upper blob"),
				Annotations: map[string]string{labels.LabelUncompressed: digest.FromString("upper diff").String()},
			}
			if snapshotter == "stargz" {
				upperDesc.Annotations[estargz.TOCJSONDigestAnnotation] = digest.FromString("upper toc").String()
			}
			descHandlers := DescHandlers{baseDesc.Digest: &DescHandler{}, upperDesc.Digest: &DescHandler{}}
			base, err := co.manager.GetByBlob(ctx, baseDesc, nil, descHandlers)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, base.Release(context.WithoutCancel(ctx))) })
			upper, err := co.manager.GetByBlob(ctx, upperDesc, base, descHandlers)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, upper.Release(context.WithoutCancel(ctx))) })
			diff, err := co.manager.Diff(ctx, base, upper, nil)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, diff.Release(context.WithoutCancel(ctx))) })
			ir := diff.(*immutableRef)
			require.True(t, ir.diffReusesUpper())
			filter := map[string]struct{}{base.ID(): {}, upper.ID(): {}}
			require.NoError(t, ir.computeChainMetadata(ctx, filter))

			// This diff exports only upper's blob, not the base beneath upper.
			upperRef := upper.(*immutableRef)
			expectedBlobChain := imagespecidentity.ChainID([]digest.Digest{upperRef.getBlob(), upperRef.getDiffID()})
			require.Equal(t, upperRef.layerIdentity(), ir.getChainID(), "diff chain must describe one layer")
			require.Equal(t, expectedBlobChain, ir.getBlobChainID(), "diff blob chain must describe one layer")
			require.NotEqual(t, upperRef.getChainID(), ir.getChainID())
			// A single-layer import without a parent can still reuse this Diff.
			reused, err := co.manager.GetByBlob(ctx, upperDesc, nil, descHandlers)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, reused.Release(context.WithoutCancel(ctx))) })
			require.Equal(t, diff.ID(), reused.ID())

			merged, err := co.manager.Merge(ctx, []ImmutableRef{base, diff}, nil)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, merged.Release(context.WithoutCancel(ctx))) })
			mergedRef := merged.(*immutableRef)
			require.NoError(t, mergedRef.computeChainMetadata(ctx, filter))
			require.Equal(t, upperRef.getChainID(), mergedRef.getChainID(), "merge must retain the base")
			require.Equal(t, upperRef.getBlobChainID(), mergedRef.getBlobChainID())
		})
	}
}

func TestGetByBlobIgnoresLegacyBlobReusingDiff(t *testing.T) {
	for _, lookup := range []string{"blob-chain", "chain-only", "legacy-parent", "descendant-blob-chain", "descendant-chain-only"} {
		t.Run(lookup, func(t *testing.T) {
			ctx := namespaces.WithNamespace(t.Context(), "buildkit-test")
			co, cleanup, err := newCacheManager(ctx, t, cmOpt{})
			require.NoError(t, err)
			t.Cleanup(cleanup)
			cm := co.manager.(*cacheManager)
			baseDesc := ocispecs.Descriptor{Digest: digest.FromString("base blob"),
				Annotations: map[string]string{labels.LabelUncompressed: digest.FromString("base diff").String()}}
			upperDesc := ocispecs.Descriptor{Digest: digest.FromString("upper blob"),
				Annotations: map[string]string{labels.LabelUncompressed: digest.FromString("upper diff").String()}}
			requested := upperDesc
			if strings.HasSuffix(lookup, "chain-only") {
				// A different compression of the same layer uses chain fallback.
				requested.Digest = digest.FromString("alternate upper blob")
			}
			descHandlers := DescHandlers{baseDesc.Digest: &DescHandler{}, upperDesc.Digest: &DescHandler{}, requested.Digest: &DescHandler{}}
			base, err := cm.GetByBlob(ctx, baseDesc, nil, descHandlers)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, base.Release(context.WithoutCancel(ctx))) })
			upper, err := cm.GetByBlob(ctx, upperDesc, base, descHandlers)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, upper.Release(context.WithoutCancel(ctx))) })
			upperRef := upper.(*immutableRef)

			// Persist an old Diff with upper's full-chain IDs, and give it an
			// ID before the normal random IDs so the bad match is deterministic.
			legacyID := "!legacy-blob-reusing-diff"
			parents := parentRefs{diffParents: &diffParents{lower: base.(*immutableRef).clone(), upper: upperRef.clone()}}
			var legacy *immutableRef
			func() {
				cm.mu.Lock()
				defer cm.mu.Unlock()
				md, _ := cm.getMetadata(legacyID)
				rec := &cacheRecord{cm: cm, mu: &sync.Mutex{}, refs: make(map[ref]struct{}),
					parentRefs: parents, cacheMetadata: md}
				require.NoError(t, initializeMetadata(md, parents))
				rec.queueSnapshotID(legacyID)
				rec.queueCommitted(true)
				rec.queueChainID(upperRef.getChainID())
				rec.queueBlobChainID(upperRef.getBlobChainID())
				require.NoError(t, rec.commitMetadata())
				cm.records[legacyID] = rec
				legacy = rec.ref(false, descHandlers, nil)
			}()
			t.Cleanup(func() { require.NoError(t, legacy.Release(context.WithoutCancel(ctx))) })
			require.True(t, legacy.diffReusesUpper())
			require.Less(t, legacyID, upper.ID())
			if lookup == "legacy-parent" {
				resolved, err := cm.GetByBlob(ctx, requested, legacy, descHandlers)
				if resolved != nil {
					t.Cleanup(func() { require.NoError(t, resolved.Release(context.WithoutCancel(ctx))) })
				}
				require.ErrorContains(t, err, "non-addressable parent")
				return
			}
			parent, expected := base, upperRef
			if strings.HasPrefix(lookup, "descendant-") {
				child := ocispecs.Descriptor{Digest: digest.FromString("child blob"),
					Annotations: map[string]string{labels.LabelUncompressed: digest.FromString("child diff").String()}}
				descHandlers[child.Digest] = &DescHandler{}
				good, err := cm.GetByBlob(ctx, child, upper, descHandlers)
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, good.Release(context.WithoutCancel(ctx))) })
				expected = good.(*immutableRef)
				parent = upper
				requested = child
				if strings.HasSuffix(lookup, "chain-only") {
					requested.Digest = digest.FromString("alternate child blob")
					descHandlers[requested.Digest] = &DescHandler{}
				}
				legacyID = "!legacy-descendant"
				// An old import could add a layer atop the truncated Diff,
				// retaining full-chain IDs while its effective chain is shorter.
				func() {
					cm.mu.Lock()
					defer cm.mu.Unlock()
					md, _ := cm.getMetadata(legacyID)
					parents := parentRefs{layerParent: legacy.clone()}
					rec := &cacheRecord{cm: cm, mu: &sync.Mutex{}, refs: make(map[ref]struct{}), parentRefs: parents, cacheMetadata: md}
					require.NoError(t, initializeMetadata(md, parents))
					rec.queueSnapshotID(legacyID)
					rec.queueCommitted(true)
					rec.queueDiffID(digest.Digest(child.Annotations[labels.LabelUncompressed]))
					rec.queueLayerIdentity(digest.Digest(child.Annotations[labels.LabelUncompressed]))
					rec.queueBlob(child.Digest)
					rec.queueChainID(expected.getChainID())
					rec.queueBlobChainID(expected.getBlobChainID())
					require.NoError(t, rec.commitMetadata())
					cm.records[legacyID] = rec
				}()
			}
			resolved, err := cm.GetByBlob(ctx, requested, parent, descHandlers)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, resolved.Release(context.WithoutCancel(ctx))) })
			require.NotEqual(t, legacyID, resolved.ID(), "must not return a Diff containing only the top layer")
			require.Equal(t, expected.getSnapshotID(), resolved.(*immutableRef).getSnapshotID(), "must not link to a legacy truncated snapshot")
		})
	}
}
