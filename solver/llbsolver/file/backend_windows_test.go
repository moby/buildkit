//go:build windows

package file

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPlatformCopy_RootOnlyProtectedExcludes(t *testing.T) {
	ctx := context.Background()

	srcRoot := t.TempDir()
	destRoot := t.TempDir()

	// Root has protected folders + a normal folder.
	require.NoError(t, os.MkdirAll(filepath.Join(srcRoot, "foo"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(srcRoot, "foo", "ok.txt"), []byte("ok"), 0o644))

	require.NoError(t, os.MkdirAll(filepath.Join(srcRoot, "System Volume Information"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(srcRoot, "System Volume Information", "root.txt"), []byte("root"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(srcRoot, "WcSandboxState"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(srcRoot, "WcSandboxState", "root.txt"), []byte("root"), 0o644))

	// Copying from mount root should exclude protected folders.
	require.NoError(t, platformCopy(ctx, srcRoot, "/", destRoot, "/"))

	_, err := os.Stat(filepath.Join(destRoot, "foo", "ok.txt"))
	require.NoError(t, err)

	_, err = os.Stat(filepath.Join(destRoot, "System Volume Information"))
	require.Error(t, err)
	require.True(t, os.IsNotExist(err))

	_, err = os.Stat(filepath.Join(destRoot, "WcSandboxState"))
	require.Error(t, err)
	require.True(t, os.IsNotExist(err))

	// Copying from a subdir should not add those excludes.
	destRoot2 := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(srcRoot, "foo", "System Volume Information"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(srcRoot, "foo", "System Volume Information", "nested.txt"), []byte("nested"), 0o644))

	require.NoError(t, platformCopy(ctx, srcRoot, "/foo", destRoot2, "/"))

	// Depending on copy semantics, the directory may land at dest root or under foo.
	_, err1 := os.Stat(filepath.Join(destRoot2, "System Volume Information", "nested.txt"))
	_, err2 := os.Stat(filepath.Join(destRoot2, "foo", "System Volume Information", "nested.txt"))
	require.True(t, err1 == nil || err2 == nil, "expected nested protected folder to be copied on non-root copy")
}
