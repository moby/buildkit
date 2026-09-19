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

func TestUnpackResolvesSymlinksBeforeParentTraversal(t *testing.T) {
	for _, target := range []string{
		"alias/../out",
		"/alias/../out",
		"missing/../alias/../out",
		"alias/../../real/out",
		"/alias/../../real/out",
	} {
		for _, suffix := range []string{"file", "new/child/file"} {
			t.Run(target+"/"+suffix, func(t *testing.T) {
				dest := t.TempDir()
				require.NoError(t, os.MkdirAll(filepath.Join(dest, "real", "child"), 0o755))
				require.NoError(t, os.Mkdir(filepath.Join(dest, "real", "out"), 0o755))
				require.NoError(t, os.Symlink("real/child", filepath.Join(dest, "alias")))

				buf := bytes.NewBuffer(nil)
				tw := tar.NewWriter(buf)
				require.NoError(t, tw.WriteHeader(&tar.Header{
					Name:     "link",
					Typeflag: tar.TypeSymlink,
					Linkname: target,
					Mode:     0o777,
				}))
				writeTarFile(t, tw, "link/"+suffix, "content")
				require.NoError(t, tw.WriteHeader(&tar.Header{
					Name:     "hardlink",
					Typeflag: tar.TypeLink,
					Linkname: "link/" + suffix,
					Mode:     0o644,
				}))
				require.NoError(t, tw.Close())

				require.NoError(t, applyArchiveNoSameOwner(t, dest, buf.Bytes()))
				file := filepath.Join(dest, "real", "out", suffix)
				dt, err := os.ReadFile(file)
				require.NoError(t, err)
				require.Equal(t, "content", string(dt))
				fi, err := os.Stat(file)
				require.NoError(t, err)
				hi, err := os.Stat(filepath.Join(dest, "hardlink"))
				require.NoError(t, err)
				require.True(t, os.SameFile(fi, hi))
				_, err = os.Lstat(filepath.Join(dest, "out"))
				require.ErrorIs(t, err, os.ErrNotExist)
			})
		}
	}
}

func TestUnpackRejectsParentTraversalThroughRegularFile(t *testing.T) {
	dest := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dest, "regular"), []byte("keep"), 0o644))
	require.NoError(t, os.Symlink("regular/../out", filepath.Join(dest, "link")))

	buf := bytes.NewBuffer(nil)
	tw := tar.NewWriter(buf)
	writeTarFile(t, tw, "link/file", "content")
	require.NoError(t, tw.Close())

	require.ErrorIs(t, applyArchiveNoSameOwner(t, dest, buf.Bytes()), unix.ENOTDIR)
	_, err := os.Lstat(filepath.Join(dest, "out"))
	require.ErrorIs(t, err, os.ErrNotExist)
	dt, err := os.ReadFile(filepath.Join(dest, "regular"))
	require.NoError(t, err)
	require.Equal(t, "keep", string(dt))
}

func TestUnpackRejectsEscapeHiddenByParentTraversal(t *testing.T) {
	for _, target := range []string{"escape/../out", "/escape/../out", "missing/../escape/../out"} {
		t.Run(target, func(t *testing.T) {
			base := t.TempDir()
			dest := filepath.Join(base, "dest")
			require.NoError(t, os.Mkdir(dest, 0o755))
			require.NoError(t, os.Symlink("..", filepath.Join(dest, "escape")))
			require.NoError(t, os.Symlink(target, filepath.Join(dest, "link")))

			buf := bytes.NewBuffer(nil)
			tw := tar.NewWriter(buf)
			writeTarFile(t, tw, "link/file", "content")
			require.NoError(t, tw.Close())

			require.ErrorContains(t, applyArchiveNoSameOwner(t, dest, buf.Bytes()), "outside extraction root")
			entries, err := os.ReadDir(base)
			require.NoError(t, err)
			require.Len(t, entries, 1)
			_, err = os.Lstat(filepath.Join(dest, "out"))
			require.ErrorIs(t, err, os.ErrNotExist)
		})
	}
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

func TestUnpackDirectoryReplacesLeafSymlink(t *testing.T) {
	for _, source := range []string{"preexisting", "archive"} {
		for _, target := range []string{"target", "/target", "missing", "../outside", "link"} {
			t.Run(source+"/"+target, func(t *testing.T) {
				base := t.TempDir()
				dest := filepath.Join(base, "dest")
				require.NoError(t, os.Mkdir(dest, 0o755))
				originalTime := time.Unix(100, 0)
				dirTime := time.Unix(200, 0)
				for _, dir := range []string{filepath.Join(dest, "target"), filepath.Join(base, "outside")} {
					require.NoError(t, os.Mkdir(dir, 0o755))
					require.NoError(t, os.Chmod(dir, 0o755))
					require.NoError(t, os.WriteFile(filepath.Join(dir, "sentinel"), []byte("untouched"), 0o644))
					require.NoError(t, os.Chtimes(dir, originalTime, originalTime))
				}

				buf := bytes.NewBuffer(nil)
				tw := tar.NewWriter(buf)
				if source == "preexisting" {
					require.NoError(t, os.Symlink(target, filepath.Join(dest, "link")))
				} else {
					require.NoError(t, tw.WriteHeader(&tar.Header{
						Name:     "link",
						Typeflag: tar.TypeSymlink,
						Linkname: target,
						Mode:     0o777,
					}))
				}
				require.NoError(t, tw.WriteHeader(&tar.Header{
					Name:     "link/",
					Typeflag: tar.TypeDir,
					Mode:     0o700,
					ModTime:  dirTime,
				}))
				writeTarFile(t, tw, "link/file", "content")
				require.NoError(t, tw.Close())

				require.NoError(t, applyArchiveNoSameOwner(t, dest, buf.Bytes()))
				fi, err := os.Lstat(filepath.Join(dest, "link"))
				require.NoError(t, err)
				require.True(t, fi.IsDir(), "directory header must replace the leaf symlink")
				require.Equal(t, os.FileMode(0o700), fi.Mode().Perm())
				require.Equal(t, dirTime, fi.ModTime())
				dt, err := os.ReadFile(filepath.Join(dest, "link", "file"))
				require.NoError(t, err)
				require.Equal(t, "content", string(dt))

				for _, dir := range []string{filepath.Join(dest, "target"), filepath.Join(base, "outside")} {
					fi, err := os.Stat(dir)
					require.NoError(t, err)
					require.Equal(t, os.FileMode(0o755), fi.Mode().Perm())
					require.Equal(t, originalTime, fi.ModTime())
					entries, err := os.ReadDir(dir)
					require.NoError(t, err)
					require.Len(t, entries, 1)
					dt, err := os.ReadFile(filepath.Join(dir, "sentinel"))
					require.NoError(t, err)
					require.Equal(t, "untouched", string(dt))
				}
				_, err = os.Lstat(filepath.Join(dest, "missing"))
				require.ErrorIs(t, err, os.ErrNotExist)
			})
		}
	}
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
