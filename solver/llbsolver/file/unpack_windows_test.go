//go:build windows

package file

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

func TestUnpackWindowsRootRelativeSymlink(t *testing.T) {
	for _, source := range []string{"preexisting", "archive"} {
		for _, target := range []string{"/target", `\target`} {
			t.Run(source+"/"+target, func(t *testing.T) {
				dest := t.TempDir()
				require.NoError(t, os.Mkdir(filepath.Join(dest, "var"), 0o755))
				buf := &bytes.Buffer{}
				tw := tar.NewWriter(buf)
				if source == "preexisting" {
					require.NoError(t, os.Symlink(target, filepath.Join(dest, "var", "link")))
				} else {
					require.NoError(t, tw.WriteHeader(&tar.Header{
						Name:     "var/link",
						Typeflag: tar.TypeSymlink,
						Linkname: target,
						Mode:     0o777,
					}))
				}
				require.NoError(t, tw.WriteHeader(&tar.Header{
					Name:     "var/link/child",
					Typeflag: tar.TypeDir,
					Mode:     0o755,
				}))
				writeTarFile(t, tw, "var/link/child/file", "content")
				require.NoError(t, tw.WriteHeader(&tar.Header{
					Name:     "hardlink",
					Typeflag: tar.TypeLink,
					Linkname: "var/link/child/file",
					Mode:     0o644,
				}))
				require.NoError(t, tw.Close())

				require.NoError(t, applyArchiveNoSameOwner(t, dest, buf.Bytes()))
				file := filepath.Join(dest, "target", "child", "file")
				dt, err := os.ReadFile(file)
				require.NoError(t, err)
				require.Equal(t, "content", string(dt))
				fi, err := os.Stat(file)
				require.NoError(t, err)
				hi, err := os.Stat(filepath.Join(dest, "hardlink"))
				require.NoError(t, err)
				require.True(t, os.SameFile(fi, hi))
				link, err := os.Readlink(filepath.Join(dest, "var", "link"))
				require.NoError(t, err)
				expectedLink := target
				if source == "preexisting" {
					expectedLink = filepath.FromSlash(target)
				}
				require.Equal(t, expectedLink, link)
				_, err = os.Lstat(filepath.Join(dest, "var", "target"))
				require.ErrorIs(t, err, os.ErrNotExist)
			})
		}
	}
}

func TestUnpackRejectsWindowsSymlinkTargetsOutsideRoot(t *testing.T) {
	for _, target := range []string{
		`C:\outside`, "C:/outside", `C:outside`,
		`\\server\share\outside`, "//server/share/outside",
		`\\?\C:\outside`, "//?/C:/outside",
		`\..\outside`, "/../outside",
	} {
		t.Run(target, func(t *testing.T) {
			dest := t.TempDir()
			require.NoError(t, os.Mkdir(filepath.Join(dest, "var"), 0o755))
			linkPtr, err := windows.UTF16PtrFromString(filepath.Join(dest, "var", "link"))
			require.NoError(t, err)
			targetPtr, err := windows.UTF16PtrFromString(target)
			require.NoError(t, err)
			// Avoid os.Symlink's target stat, which could contact the UNC server.
			const allowUnprivilegedCreate = 0x2
			require.NoError(t, windows.CreateSymbolicLink(linkPtr, targetPtr, windows.SYMBOLIC_LINK_FLAG_DIRECTORY|allowUnprivilegedCreate))
			buf := &bytes.Buffer{}
			tw := tar.NewWriter(buf)
			writeTarFile(t, tw, "var/link/file", "content")
			require.NoError(t, tw.Close())

			require.ErrorContains(t, applyArchiveNoSameOwner(t, dest, buf.Bytes()), "outside extraction root")
			_, err = os.Lstat(filepath.Join(dest, "outside"))
			require.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}

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
