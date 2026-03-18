package container

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsPathEscapesRootError(t *testing.T) {
	tmpdir := t.TempDir()

	// Create a symlink that would cause the path to escape the root.
	err := os.Symlink("/bin/sh", filepath.Join(tmpdir, "sh"))
	require.NoError(t, err)

	// Open a root filesystem.
	fsys, err := os.OpenRoot(tmpdir)
	require.NoError(t, err)
	defer fsys.Close()

	// Attempt to stat the sh file which should try to escape the root.
	_, err = fs.Stat(fsys.FS(), "sh")
	require.Error(t, err)

	require.True(t, isPathEscapesRootError(err), "expected returned error to be a path escapes root error")
}
