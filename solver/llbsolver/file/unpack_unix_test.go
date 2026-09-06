//go:build !windows

package file

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
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

func TestUnpackWritesThroughRelativeArchiveSymlinkInsideRoot(t *testing.T) {
	dest := t.TempDir()

	buf := bytes.NewBuffer(nil)
	tw := tar.NewWriter(buf)
	for _, name := range []string{"dir", "target"} {
		require.NoError(t, tw.WriteHeader(&tar.Header{
			Name:     name,
			Typeflag: tar.TypeDir,
			Mode:     0o755,
		}))
	}
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name:     "dir/link",
		Typeflag: tar.TypeSymlink,
		Linkname: "../target",
		Mode:     0o777,
	}))
	writeTarFile(t, tw, "dir/link/file", "content")
	require.NoError(t, tw.Close())

	require.NoError(t, applyArchiveNoSameOwner(t, dest, buf.Bytes()))

	dt, err := os.ReadFile(filepath.Join(dest, "target", "file"))
	require.NoError(t, err)
	require.Equal(t, "content", string(dt))
}

func TestUnpackRejectsNestedRelativeArchiveSymlinkEscape(t *testing.T) {
	parent := t.TempDir()
	dest := filepath.Join(parent, "dest")
	outside := filepath.Join(parent, "outside")
	require.NoError(t, os.Mkdir(dest, 0o755))
	require.NoError(t, os.Mkdir(outside, 0o755))

	buf := bytes.NewBuffer(nil)
	tw := tar.NewWriter(buf)
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name:     "dir",
		Typeflag: tar.TypeDir,
		Mode:     0o755,
	}))
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name:     "dir/escape",
		Typeflag: tar.TypeSymlink,
		Linkname: "../../outside",
		Mode:     0o777,
	}))
	writeTarFile(t, tw, "dir/escape/pwned", "pwned")
	require.NoError(t, tw.Close())

	require.Error(t, applyArchiveNoSameOwner(t, dest, buf.Bytes()))

	_, err := os.Stat(filepath.Join(outside, "pwned"))
	require.True(t, os.IsNotExist(err), "nested archive-created symlink escaped destination")
}

func TestUnpackRejectsTwoHopSymlinkBreakout(t *testing.T) {
	parent := t.TempDir()
	dest := filepath.Join(parent, "dest")
	victim := filepath.Join(parent, "victim")
	require.NoError(t, os.Mkdir(dest, 0o755))
	require.NoError(t, os.Mkdir(victim, 0o755))

	buf := bytes.NewBuffer(nil)
	tw := tar.NewWriter(buf)
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name:     "inner",
		Typeflag: tar.TypeDir,
		Mode:     0o755,
	}))
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name:     "inner/go_up",
		Typeflag: tar.TypeSymlink,
		Linkname: "..",
		Mode:     0o777,
	}))
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name:     "inner/go_up/escape",
		Typeflag: tar.TypeSymlink,
		Linkname: "../victim",
		Mode:     0o777,
	}))
	writeTarFile(t, tw, "inner/go_up/escape/newfile", "pwned")
	require.NoError(t, tw.Close())

	require.Error(t, applyArchiveNoSameOwner(t, dest, buf.Bytes()))

	_, err := os.Lstat(filepath.Join(victim, "newfile"))
	require.True(t, os.IsNotExist(err), "two-hop symlink chain escaped destination")
}

func TestUnpackRejectsRelativeEscapeBeforeAbsoluteSymlink(t *testing.T) {
	dest := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dest, "target"), 0o755))
	require.NoError(t, os.Symlink("..", filepath.Join(dest, "escape")))
	require.NoError(t, os.Symlink("/target", filepath.Join(dest, "absolute")))

	buf := bytes.NewBuffer(nil)
	tw := tar.NewWriter(buf)
	writeTarFile(t, tw, "escape/absolute/file", "content")
	require.NoError(t, tw.Close())

	require.Error(t, applyArchiveNoSameOwner(t, dest, buf.Bytes()))

	_, err := os.Lstat(filepath.Join(dest, "target", "file"))
	require.True(t, os.IsNotExist(err), "relative escape should not be hidden by a later absolute symlink")
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

func TestUnpackRejectsHardlinkThroughEscapingSymlink(t *testing.T) {
	parent := t.TempDir()
	dest := filepath.Join(parent, "dest")
	outside := filepath.Join(parent, "outside")
	require.NoError(t, os.Mkdir(dest, 0o755))
	require.NoError(t, os.Mkdir(outside, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(outside, "target"), []byte("target"), 0o644))
	require.NoError(t, os.Symlink("../outside", filepath.Join(dest, "escape")))

	buf := bytes.NewBuffer(nil)
	tw := tar.NewWriter(buf)
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name:     "hardlink",
		Typeflag: tar.TypeLink,
		Linkname: "escape/target",
		Mode:     0o644,
	}))
	require.NoError(t, tw.Close())

	require.Error(t, applyArchiveNoSameOwner(t, dest, buf.Bytes()))

	_, err := os.Lstat(filepath.Join(dest, "hardlink"))
	require.True(t, os.IsNotExist(err), "hardlink through escaping symlink should not be extracted")
}

func TestUnpackReplacesArchiveSymlinkFinalPath(t *testing.T) {
	parent := t.TempDir()
	dest := filepath.Join(parent, "dest")
	outside := filepath.Join(parent, "outside")
	require.NoError(t, os.Mkdir(dest, 0o755))
	require.NoError(t, os.Mkdir(outside, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(outside, "target"), []byte("target"), 0o644))

	buf := bytes.NewBuffer(nil)
	tw := tar.NewWriter(buf)
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name:     "escape",
		Typeflag: tar.TypeSymlink,
		Linkname: "../outside/target",
		Mode:     0o777,
	}))
	writeTarFile(t, tw, "escape", "content")
	require.NoError(t, tw.Close())

	require.NoError(t, applyArchiveNoSameOwner(t, dest, buf.Bytes()))

	dt, err := os.ReadFile(filepath.Join(dest, "escape"))
	require.NoError(t, err)
	require.Equal(t, "content", string(dt))

	dt, err = os.ReadFile(filepath.Join(outside, "target"))
	require.NoError(t, err)
	require.Equal(t, "target", string(dt))
}

func TestUnpackRejectsSymlinkLoop(t *testing.T) {
	dest := t.TempDir()

	buf := bytes.NewBuffer(nil)
	tw := tar.NewWriter(buf)
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name:     "a",
		Typeflag: tar.TypeSymlink,
		Linkname: "b",
		Mode:     0o777,
	}))
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name:     "b",
		Typeflag: tar.TypeSymlink,
		Linkname: "a",
		Mode:     0o777,
	}))
	writeTarFile(t, tw, "a/file", "content")
	require.NoError(t, tw.Close())

	require.ErrorContains(t, applyArchiveNoSameOwner(t, dest, buf.Bytes()), "too many symlinks")

	_, err := os.Lstat(filepath.Join(dest, "a", "file"))
	require.Error(t, err, "symlink loop should not create a file")
}

func TestUnpackReplacesPreexistingFinalSymlink(t *testing.T) {
	parent := t.TempDir()
	dest := filepath.Join(parent, "dest")
	outside := filepath.Join(parent, "outside")
	require.NoError(t, os.Mkdir(dest, 0o755))
	require.NoError(t, os.Mkdir(outside, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(outside, "target"), []byte("target"), 0o644))
	require.NoError(t, os.Symlink("../outside/target", filepath.Join(dest, "link")))

	buf := bytes.NewBuffer(nil)
	tw := tar.NewWriter(buf)
	writeTarFile(t, tw, "link", "content")
	require.NoError(t, tw.Close())

	require.NoError(t, applyArchiveNoSameOwner(t, dest, buf.Bytes()))

	dt, err := os.ReadFile(filepath.Join(dest, "link"))
	require.NoError(t, err)
	require.Equal(t, "content", string(dt))

	dt, err = os.ReadFile(filepath.Join(outside, "target"))
	require.NoError(t, err)
	require.Equal(t, "target", string(dt))
}

func TestUnpackDirectoryModeIgnoresUmask(t *testing.T) {
	oldUmask := unix.Umask(0o077)
	defer unix.Umask(oldUmask)

	dest := t.TempDir()

	buf := bytes.NewBuffer(nil)
	tw := tar.NewWriter(buf)
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name:     "tmp",
		Typeflag: tar.TypeDir,
		Mode:     0o1777,
	}))
	require.NoError(t, tw.Close())

	require.NoError(t, applyArchiveNoSameOwner(t, dest, buf.Bytes()))

	fi, err := os.Lstat(filepath.Join(dest, "tmp"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o777), fi.Mode().Perm())
	require.NotZero(t, fi.Mode()&os.ModeSticky)
}

func TestUnpackImpliedDirectoryModeIgnoresUmask(t *testing.T) {
	oldUmask := unix.Umask(0o077)
	defer unix.Umask(oldUmask)

	dest := t.TempDir()

	buf := bytes.NewBuffer(nil)
	tw := tar.NewWriter(buf)
	writeTarFile(t, tw, "deeply/nested/file", "content")
	require.NoError(t, tw.Close())

	require.NoError(t, applyArchiveNoSameOwner(t, dest, buf.Bytes()))

	for _, name := range []string{"deeply", "deeply/nested"} {
		fi, err := os.Lstat(filepath.Join(dest, name))
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o755), fi.Mode().Perm())
	}
}

func TestUnpackSetsSymlinkTimes(t *testing.T) {
	dest := t.TempDir()
	modTime := time.Unix(123, 0)

	buf := bytes.NewBuffer(nil)
	tw := tar.NewWriter(buf)
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name:       "link",
		Typeflag:   tar.TypeSymlink,
		Linkname:   "target",
		Mode:       0o777,
		AccessTime: modTime,
		ModTime:    modTime,
	}))
	require.NoError(t, tw.Close())

	require.NoError(t, applyArchiveNoSameOwner(t, dest, buf.Bytes()))

	fi, err := os.Lstat(filepath.Join(dest, "link"))
	require.NoError(t, err)
	require.True(t, fi.ModTime().Equal(modTime), "expected %s, got %s", modTime, fi.ModTime())
}
