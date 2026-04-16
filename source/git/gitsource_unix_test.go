//go:build !windows
// +build !windows

package git

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestReaddirnames verifies that readdirnames correctly lists directory entries
// from an O_PATH fd returned by openSubdirSafe.
func TestReaddirnames(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	require.NoError(t, os.MkdirAll(filepath.Join(root, "sub"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "sub", "a.txt"), []byte("a"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "sub", "b.txt"), []byte("b"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "sub", "child"), 0o755))

	f, err := openSubdirSafe(root, "sub")
	require.NoError(t, err)
	defer f.Close()

	names, err := readdirnames(f)
	require.NoError(t, err)

	sort.Strings(names)
	require.Equal(t, []string{"a.txt", "b.txt", "child"}, names)
}
