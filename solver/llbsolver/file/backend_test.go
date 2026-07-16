package file

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/moby/buildkit/solver/pb"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestRmPathNonExistentFileAllowNotFoundFalse(t *testing.T) {
	root := t.TempDir()
	err := rmPath(root, "doesnt_exist", false)
	require.Error(t, err)
	require.True(t, errors.Is(err, os.ErrNotExist))
}

func TestRmPathNonExistentFileAllowNotFoundTrue(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, rmPath(root, "doesnt_exist", true))
}

func TestRmPathFileExists(t *testing.T) {
	root := t.TempDir()

	src := filepath.Join(root, "exists")
	file, err := os.Create(src)
	require.NoError(t, err)
	file.Close()

	require.NoError(t, rmPath(root, "exists", false))

	_, err = os.Stat(src)

	require.True(t, os.IsNotExist(err))
}

func TestRmParentTraversalDoesNotEscapeRoot(t *testing.T) {
	// Backslash variants are separators on Windows (real traversal there) and
	// ordinary filename characters on Linux (inert, but still must not escape).
	for _, p := range []string{
		"..", "../..", "a/../..", "../victim",
		"..\\..", "a\\..\\..", "..\\victim",
	} {
		t.Run(p, func(t *testing.T) {
			parent := t.TempDir()
			root := filepath.Join(parent, "root")
			victim := filepath.Join(parent, "victim")

			require.NoError(t, os.Mkdir(root, 0o755))
			require.NoError(t, os.Mkdir(victim, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(victim, "data"), []byte("data"), 0o644))

			require.Error(t, rm(root, &pb.FileActionRm{Path: p}))

			_, err := os.Stat(victim)
			require.NoError(t, err, "rm escaped root and deleted sibling %q", victim)
		})
	}
}

func TestRmPathRemovesSymlinkItself(t *testing.T) {
	root := t.TempDir()

	target := filepath.Join(root, "target")
	link := filepath.Join(root, "link")

	require.NoError(t, os.WriteFile(target, []byte("target"), 0o644))
	require.NoError(t, os.Symlink("target", link))

	require.NoError(t, rmPath(root, "link", false))

	_, err := os.Lstat(link)
	require.True(t, os.IsNotExist(err))

	_, err = os.Stat(target)
	require.NoError(t, err)
}
