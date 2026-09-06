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

func TestUnpackRejectsWindowsVolumePath(t *testing.T) {
	parent := t.TempDir()
	dest := filepath.Join(parent, "dest")
	require.NoError(t, os.Mkdir(dest, 0o755))

	buf := bytes.NewBuffer(nil)
	tw := tar.NewWriter(buf)
	writeTarFile(t, tw, filepath.VolumeName(parent)+`/pwned`, "pwned")
	require.NoError(t, tw.Close())

	require.Error(t, applyArchiveNoSameOwner(t, dest, buf.Bytes()))

	_, err := os.Stat(filepath.Join(dest, "pwned"))
	require.True(t, os.IsNotExist(err), "volume-qualified archive path was extracted")
}

func TestUnpackRejectsWindowsBackslashPath(t *testing.T) {
	dest := t.TempDir()

	buf := bytes.NewBuffer(nil)
	tw := tar.NewWriter(buf)
	writeTarFile(t, tw, `dir\file`, "content")
	require.NoError(t, tw.Close())

	require.Error(t, applyArchiveNoSameOwner(t, dest, buf.Bytes()))

	_, err := os.Stat(filepath.Join(dest, "dir", "file"))
	require.True(t, os.IsNotExist(err), "backslash tar path was interpreted as a Windows path")
}

func TestUnpackRejectsWindowsBackslashHardlinkTarget(t *testing.T) {
	dest := t.TempDir()

	buf := bytes.NewBuffer(nil)
	tw := tar.NewWriter(buf)
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name:     "hardlink",
		Typeflag: tar.TypeLink,
		Linkname: `dir\file`,
		Mode:     0o644,
	}))
	require.NoError(t, tw.Close())

	require.Error(t, applyArchiveNoSameOwner(t, dest, buf.Bytes()))

	_, err := os.Stat(filepath.Join(dest, "hardlink"))
	require.True(t, os.IsNotExist(err), "backslash hardlink target was interpreted as a Windows path")
}
