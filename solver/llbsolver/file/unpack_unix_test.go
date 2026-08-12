//go:build !windows

package file

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUnpackWritesThroughRootLocalAbsoluteSymlink(t *testing.T) {
	dest := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dest, "run"), 0o755))
	require.NoError(t, os.Mkdir(filepath.Join(dest, "var"), 0o755))
	require.NoError(t, os.Symlink("/run", filepath.Join(dest, "var", "run")))

	buf := bytes.NewBuffer(nil)
	tw := tar.NewWriter(buf)
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name:     "var/run/act",
		Typeflag: tar.TypeDir,
		Mode:     0o755,
	}))
	writeTarFile(t, tw, "var/run/act/file", "content")
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name:     "var/run/act/file.link",
		Typeflag: tar.TypeLink,
		Linkname: "var/run/act/file",
		Mode:     0o644,
	}))
	require.NoError(t, tw.Close())

	require.NoError(t, applyArchiveNoSameOwner(t, dest, buf.Bytes()))

	dt, err := os.ReadFile(filepath.Join(dest, "run", "act", "file"))
	require.NoError(t, err)
	require.Equal(t, "content", string(dt))

	link, err := os.Readlink(filepath.Join(dest, "var", "run"))
	require.NoError(t, err)
	require.Equal(t, "/run", link)

	fileInfo, err := os.Stat(filepath.Join(dest, "run", "act", "file"))
	require.NoError(t, err)
	linkInfo, err := os.Stat(filepath.Join(dest, "run", "act", "file.link"))
	require.NoError(t, err)
	require.True(t, os.SameFile(fileInfo, linkInfo))
}

func TestUnpackDoesNotWriteThroughAbsoluteArchiveSymlink(t *testing.T) {
	parent := t.TempDir()
	dest := filepath.Join(parent, "dest")
	outside := filepath.Join(parent, "outside")
	require.NoError(t, os.Mkdir(dest, 0o755))
	require.NoError(t, os.Mkdir(outside, 0o755))

	buf := bytes.NewBuffer(nil)
	tw := tar.NewWriter(buf)
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name:     "escape",
		Typeflag: tar.TypeSymlink,
		Linkname: filepath.ToSlash(outside),
		Mode:     0o777,
	}))
	writeTarFile(t, tw, "escape/pwned", "pwned")
	require.NoError(t, tw.Close())

	require.NoError(t, applyArchiveNoSameOwner(t, dest, buf.Bytes()))

	_, err := os.Stat(filepath.Join(outside, "pwned"))
	require.True(t, os.IsNotExist(err), "archive-created symlink escaped destination")
}

func TestUnpackRejectsRelativeArchiveSymlinkEscape(t *testing.T) {
	parent := t.TempDir()
	dest := filepath.Join(parent, "dest")
	outside := filepath.Join(parent, "outside")
	require.NoError(t, os.Mkdir(dest, 0o755))
	require.NoError(t, os.Mkdir(outside, 0o755))

	buf := bytes.NewBuffer(nil)
	tw := tar.NewWriter(buf)
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name:     "escape",
		Typeflag: tar.TypeSymlink,
		Linkname: "../outside",
		Mode:     0o777,
	}))
	writeTarFile(t, tw, "escape/pwned", "pwned")
	require.NoError(t, tw.Close())

	require.Error(t, applyArchiveNoSameOwner(t, dest, buf.Bytes()))

	_, err := os.Stat(filepath.Join(outside, "pwned"))
	require.True(t, os.IsNotExist(err), "archive-created symlink escaped destination")
}

func TestUnpackRejectsRelativePreexistingSymlinkEscape(t *testing.T) {
	parent := t.TempDir()
	dest := filepath.Join(parent, "dest")
	outside := filepath.Join(parent, "outside")
	require.NoError(t, os.Mkdir(dest, 0o755))
	require.NoError(t, os.Mkdir(outside, 0o755))
	require.NoError(t, os.Symlink("../outside", filepath.Join(dest, "escape")))

	buf := bytes.NewBuffer(nil)
	tw := tar.NewWriter(buf)
	writeTarFile(t, tw, "escape/pwned", "pwned")
	require.NoError(t, tw.Close())

	require.Error(t, applyArchiveNoSameOwner(t, dest, buf.Bytes()))

	_, err := os.Stat(filepath.Join(outside, "pwned"))
	require.True(t, os.IsNotExist(err), "preexisting symlink escaped destination")
}
