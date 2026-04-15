package cache

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func newTestImmutableRef(kind refKind, parent *immutableRef) *immutableRef {
	ref := &immutableRef{
		cacheRecord: &cacheRecord{
			mu: &sync.Mutex{},
		},
	}
	switch kind {
	case Layer:
		ref.layerParent = parent
	case Merge:
		ref.mergeParents = []*immutableRef{parent}
	case Diff:
		ref.diffParents = &diffParents{lower: parent}
	}
	return ref
}

func TestIsLinearLayerChain(t *testing.T) {
	base := newTestImmutableRef(BaseLayer, nil)
	layer1 := newTestImmutableRef(Layer, base)
	layer2 := newTestImmutableRef(Layer, layer1)
	merge := newTestImmutableRef(Merge, base)
	diff := newTestImmutableRef(Diff, base)

	tests := []struct {
		name  string
		chain []*immutableRef
		want  bool
	}{
		{"empty chain", nil, true},
		{"single base layer", []*immutableRef{base}, true},
		{"base + layer", []*immutableRef{base, layer1}, true},
		{"base + layer + layer", []*immutableRef{base, layer1, layer2}, true},
		{"single layer (no base)", []*immutableRef{layer1}, false},
		{"base in wrong position", []*immutableRef{layer1, base}, false},
		{"merge ref", []*immutableRef{base, merge}, false},
		{"diff ref", []*immutableRef{base, diff}, false},
		{"broken parent link", []*immutableRef{base, layer2}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isLinearLayerChain(tt.chain)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestCleanupParallelExtractDirs(t *testing.T) {
	root := t.TempDir()

	// Create dirs that should be cleaned up
	require.NoError(t, os.Mkdir(filepath.Join(root, "buildkit-parallel-extract-abc123"), 0755))
	require.NoError(t, os.Mkdir(filepath.Join(root, "buildkit-parallel-extract-xyz789"), 0755))
	// Create a file inside one to verify RemoveAll works
	require.NoError(t, os.WriteFile(filepath.Join(root, "buildkit-parallel-extract-abc123", "data"), []byte("test"), 0644))

	// Create dirs that should NOT be cleaned up
	require.NoError(t, os.Mkdir(filepath.Join(root, "snapshots"), 0755))
	require.NoError(t, os.Mkdir(filepath.Join(root, "other-dir"), 0755))

	cm := &cacheManager{root: root}
	cm.cleanupParallelExtractDirs()

	entries, err := os.ReadDir(root)
	require.NoError(t, err)

	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}

	require.NotContains(t, names, "buildkit-parallel-extract-abc123")
	require.NotContains(t, names, "buildkit-parallel-extract-xyz789")
	require.Contains(t, names, "snapshots")
	require.Contains(t, names, "other-dir")
}
