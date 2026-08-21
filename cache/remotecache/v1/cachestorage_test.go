package cacheimport

import (
	"os"
	"testing"

	"github.com/moby/buildkit/solver"
	"github.com/stretchr/testify/require"
)

// TestNewCacheKeyStorageCyclicMergeChain guards against a regression where
// addItemToStorage could silently drop a dependency link.
//
// testdata/cyclic-merge-chain.json is a real `--cache-to type=local,mode=max`
// export captured from a build where two unrelated COPY operations
// coincidentally produced byte-identical content (see the Add doc comment
// in chains.go): their cache records end up sharing a digest despite having
// distinct dependency chains. Several levels of that coincidence deep, the
// resulting graph makes NewCacheKeyStorage's traversal revisit an item that
// a different path is still in the middle of resolving.
//
// Before the fix, addItemToStorage only allocated an item's storage entry
// after fully walking its own dependencies, so a reentrant call arriving
// mid-walk found no entry yet, got nil, and the caller silently skipped
// registering its link - with no error. Which links survived depended on
// Go's randomized map iteration order (cc.leaves(), and the multi-candidate
// alternatives at a single input slot), so reconstructing the exact same
// chain from the exact same bytes could non-deterministically drop a link
// on some process runs and not others. Parsing this fixture repeatedly
// reproduces that: it drops the link on roughly 60-80% of iterations
// against the pre-fix implementation, closely matching the failure rate
// observed in the field.
func TestNewCacheKeyStorageCyclicMergeChain(t *testing.T) {
	dt, err := os.ReadFile("testdata/cyclic-merge-chain.json")
	require.NoError(t, err)

	// The selector on the specific link that a merge step's cache key uses
	// to depend on another record that coincidentally shares its digest.
	const wantSelector = "sha256:a7cb13d089672f15c02003beb688fb5642a1c62b68ffeee7c2ff29d26490deec"

	// A single iteration isn't a reliable regression check: the bug this
	// guards against is intermittent (Go randomizes map iteration order
	// per process, not per call), so run enough iterations that a
	// reintroduced regression would be virtually certain to show up.
	for i := range 40 {
		cc := NewCacheChains()
		require.NoError(t, Parse(dt, DescriptorProvider{}, cc))

		storage, _, err := NewCacheKeyStorage(cc, nil)
		require.NoError(t, err)

		found := false
		require.NoError(t, storage.Walk(func(id string) error {
			return storage.WalkBacklinks(id, func(_ string, link solver.CacheInfoLink) error {
				if link.Selector.String() == wantSelector {
					found = true
				}
				return nil
			})
		}))
		require.True(t, found, "iteration %d: link for the coincidentally-shared-digest dependency was dropped", i)
	}
}
