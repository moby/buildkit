//go:build windows

package file

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUnpackSkipsSameOwnerOnWindows(t *testing.T) {
	srcRoot := t.TempDir()
	destRoot := t.TempDir()

	buf := bytes.NewBuffer(nil)
	tw := tar.NewWriter(buf)
	writeTarFile(t, tw, "foo", "content")
	require.NoError(t, tw.Close())

	require.NoError(t, os.WriteFile(filepath.Join(srcRoot, "archive.tar"), buf.Bytes(), 0o600))

	ok, err := unpack(t.Context(), srcRoot, "archive.tar", destRoot, "/", nil, nil, nil, nil)
	require.NoError(t, err)
	require.True(t, ok)

	dt, err := os.ReadFile(filepath.Join(destRoot, "foo"))
	require.NoError(t, err)
	require.Equal(t, "content", string(dt))
}
