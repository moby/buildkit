package file

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/moby/sys/user"
	"github.com/stretchr/testify/require"
	copy "github.com/tonistiigi/fsutil/copy"
)

func TestOwnerMapperMapsArchiveHeader(t *testing.T) {
	t.Parallel()

	idmap := &user.IdentityMapping{
		UIDMaps: []user.IDMap{{ID: 0, ParentID: 1000, Count: 10}},
		GIDMaps: []user.IDMap{{ID: 0, ParentID: 2000, Count: 10}},
	}
	hdr := &tar.Header{Uid: 2, Gid: 3}

	err := mapArchiveHeaderOwner(hdr, nil, idmap)
	require.NoError(t, err)
	require.Equal(t, 1002, hdr.Uid)
	require.Equal(t, 2003, hdr.Gid)
}

func TestOwnerMapperChownOverridesArchiveHeader(t *testing.T) {
	t.Parallel()

	idmap := &user.IdentityMapping{
		UIDMaps: []user.IDMap{{ID: 0, ParentID: 1000, Count: 10}},
		GIDMaps: []user.IDMap{{ID: 0, ParentID: 2000, Count: 10}},
	}
	hdr := &tar.Header{Uid: 2, Gid: 3}

	err := mapArchiveHeaderOwner(hdr, &copy.User{UID: 5, GID: 6}, idmap)
	require.NoError(t, err)
	require.Equal(t, 5, hdr.Uid)
	require.Equal(t, 6, hdr.Gid)
}

func TestUnpackPreservesWhiteoutFiles(t *testing.T) {
	dest := t.TempDir()

	buf := bytes.NewBuffer(nil)
	tw := tar.NewWriter(buf)
	writeTarFile(t, tw, "victim", "victim")
	writeTarFile(t, tw, ".wh.victim", "whiteout")
	require.NoError(t, tw.Close())

	require.NoError(t, applyArchiveNoSameOwner(t, dest, buf.Bytes()))

	dt, err := os.ReadFile(filepath.Join(dest, "victim"))
	require.NoError(t, err)
	require.Equal(t, "victim", string(dt))

	dt, err = os.ReadFile(filepath.Join(dest, ".wh.victim"))
	require.NoError(t, err)
	require.Equal(t, "whiteout", string(dt))
}

func TestUnpackRejectsParentDirectoryPath(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
	}{
		{name: "relative parent", path: "../outside/pwned"},
		{name: "absolute parent", path: "/../outside/pwned"},
		{name: "nested parent", path: "dir/../../outside/pwned"},
		{name: "current then parent", path: "./../outside/pwned"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent := t.TempDir()
			dest := filepath.Join(parent, "dest")
			outside := filepath.Join(parent, "outside")
			require.NoError(t, os.Mkdir(dest, 0o755))
			require.NoError(t, os.Mkdir(outside, 0o755))

			buf := bytes.NewBuffer(nil)
			tw := tar.NewWriter(buf)
			writeTarFile(t, tw, tc.path, "pwned")
			require.NoError(t, tw.Close())

			require.Error(t, applyArchiveNoSameOwner(t, dest, buf.Bytes()))

			_, err := os.Stat(filepath.Join(outside, "pwned"))
			require.True(t, os.IsNotExist(err), "archive path escaped destination")
		})
	}
}

func TestUnpackRejectsBareParentDirectoryPath(t *testing.T) {
	base := t.TempDir()
	dest := filepath.Join(base, "dest")
	require.NoError(t, os.Mkdir(dest, 0o755))

	buf := bytes.NewBuffer(nil)
	tw := tar.NewWriter(buf)
	writeTarFile(t, tw, "..", "pwned")
	require.NoError(t, tw.Close())

	require.Error(t, applyArchiveNoSameOwner(t, dest, buf.Bytes()))

	entries, err := os.ReadDir(base)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "dest", entries[0].Name())
}

func TestUnpackAbsolutePathIsRelativeToDestination(t *testing.T) {
	dest := t.TempDir()

	buf := bytes.NewBuffer(nil)
	tw := tar.NewWriter(buf)
	writeTarFile(t, tw, "/absolute", "content")
	require.NoError(t, tw.Close())

	require.NoError(t, applyArchiveNoSameOwner(t, dest, buf.Bytes()))

	dt, err := os.ReadFile(filepath.Join(dest, "absolute"))
	require.NoError(t, err)
	require.Equal(t, "content", string(dt))
}

func TestUnpackDoesNotHardlinkOutsideDestination(t *testing.T) {
	for _, tc := range []struct {
		name     string
		linkname string
	}{
		{name: "relative parent", linkname: "../outside/target"},
		{name: "absolute parent", linkname: "/../outside/target"},
		{name: "absolute host path", linkname: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent := t.TempDir()
			dest := filepath.Join(parent, "dest")
			outside := filepath.Join(parent, "outside")
			require.NoError(t, os.Mkdir(dest, 0o755))
			require.NoError(t, os.Mkdir(outside, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(outside, "target"), []byte("target"), 0o644))

			linkname := tc.linkname
			if linkname == "" {
				linkname = filepath.ToSlash(filepath.Join(outside, "target"))
			}

			buf := bytes.NewBuffer(nil)
			tw := tar.NewWriter(buf)
			require.NoError(t, tw.WriteHeader(&tar.Header{
				Name:     "hardlink",
				Typeflag: tar.TypeLink,
				Linkname: linkname,
				Mode:     0o644,
			}))
			require.NoError(t, tw.Close())

			require.Error(t, applyArchiveNoSameOwner(t, dest, buf.Bytes()))

			dt, err := os.ReadFile(filepath.Join(outside, "target"))
			require.NoError(t, err)
			require.Equal(t, "target", string(dt))
		})
	}
}

func TestUnpackDoesNotHardlinkToPrefixSibling(t *testing.T) {
	base := t.TempDir()
	dest := filepath.Join(base, "dest")
	sibling := filepath.Join(base, "dest-evil")
	require.NoError(t, os.Mkdir(dest, 0o755))
	require.NoError(t, os.Mkdir(sibling, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sibling, "secret"), []byte("secret"), 0o600))

	buf := bytes.NewBuffer(nil)
	tw := tar.NewWriter(buf)
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name:     "grab",
		Typeflag: tar.TypeLink,
		Linkname: "../dest-evil/secret",
		Mode:     0o644,
	}))
	require.NoError(t, tw.Close())

	require.Error(t, applyArchiveNoSameOwner(t, dest, buf.Bytes()))

	_, err := os.Stat(filepath.Join(dest, "grab"))
	require.True(t, os.IsNotExist(err), "hardlink to prefix-sharing sibling should not be extracted")

	dt, err := os.ReadFile(filepath.Join(sibling, "secret"))
	require.NoError(t, err)
	require.Equal(t, "secret", string(dt))
}

func TestUnpackRejectsHardlinkToExtractionRoot(t *testing.T) {
	dest := t.TempDir()

	buf := bytes.NewBuffer(nil)
	tw := tar.NewWriter(buf)
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name:     "hardlink",
		Typeflag: tar.TypeLink,
		Linkname: "/",
		Mode:     0o644,
	}))
	require.NoError(t, tw.Close())

	require.Error(t, applyArchiveNoSameOwner(t, dest, buf.Bytes()))

	_, err := os.Stat(filepath.Join(dest, "hardlink"))
	require.True(t, os.IsNotExist(err), "hardlink to extraction root should not be extracted")
}

func TestUnpackAbsoluteHardlinkIsRelativeToDestination(t *testing.T) {
	dest := t.TempDir()

	buf := bytes.NewBuffer(nil)
	tw := tar.NewWriter(buf)
	writeTarFile(t, tw, "usr/bin/perlbug", "hello")
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name:     "usr/bin/perlthanks",
		Typeflag: tar.TypeLink,
		Linkname: "/usr/bin/perlbug",
		Mode:     0o755,
	}))
	require.NoError(t, tw.Close())

	require.NoError(t, applyArchiveNoSameOwner(t, dest, buf.Bytes()))

	source, err := os.Stat(filepath.Join(dest, "usr", "bin", "perlbug"))
	require.NoError(t, err)
	link, err := os.Stat(filepath.Join(dest, "usr", "bin", "perlthanks"))
	require.NoError(t, err)
	require.True(t, os.SameFile(source, link), "absolute hardlink target should resolve within destination")
}

func TestUnpackRejectsAbsoluteSymlinkRelativeEscape(t *testing.T) {
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
		Linkname: "/../outside",
		Mode:     0o777,
	}))
	writeTarFile(t, tw, "escape/pwned", "pwned")
	require.NoError(t, tw.Close())

	require.Error(t, applyArchiveNoSameOwner(t, dest, buf.Bytes()))

	_, err := os.Stat(filepath.Join(outside, "pwned"))
	require.True(t, os.IsNotExist(err), "archive-created symlink escaped destination")
}

func TestUnpackRejectsSpecialFileEntries(t *testing.T) {
	for _, tc := range []struct {
		name     string
		typeflag byte
	}{
		{name: "block", typeflag: tar.TypeBlock},
		{name: "char", typeflag: tar.TypeChar},
		{name: "fifo", typeflag: tar.TypeFifo},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dest := t.TempDir()

			buf := bytes.NewBuffer(nil)
			tw := tar.NewWriter(buf)
			require.NoError(t, tw.WriteHeader(&tar.Header{
				Name:     tc.name,
				Typeflag: tc.typeflag,
				Mode:     0o600,
			}))
			require.NoError(t, tw.Close())

			require.Error(t, applyArchiveNoSameOwner(t, dest, buf.Bytes()))

			_, err := os.Stat(filepath.Join(dest, tc.name))
			require.True(t, os.IsNotExist(err), "special entry should not be extracted")
		})
	}
}

func TestUnpackSkipsExtendedHeadersWithoutCreatingParents(t *testing.T) {
	dest := t.TempDir()

	buf := bytes.NewBuffer(nil)
	tw := tar.NewWriter(buf)
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name:     "foo/bar",
		Typeflag: tar.TypeXGlobalHeader,
	}))
	require.NoError(t, tw.Close())

	require.NoError(t, applyArchiveNoSameOwner(t, dest, buf.Bytes()))

	_, err := os.Stat(filepath.Join(dest, "foo"))
	require.True(t, os.IsNotExist(err), "extended header should not create parent directories")
}

func TestUnpackBoundsOutOfRangeTimes(t *testing.T) {
	dest := t.TempDir()
	modTime := time.Date(1800, 1, 1, 0, 0, 0, 0, time.UTC)

	buf := bytes.NewBuffer(nil)
	tw := tar.NewWriter(buf)
	content := "content"
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name:       "file",
		Typeflag:   tar.TypeReg,
		Mode:       0o644,
		Size:       int64(len(content)),
		AccessTime: modTime,
		ModTime:    modTime,
	}))
	_, err := tw.Write([]byte(content))
	require.NoError(t, err)
	require.NoError(t, tw.Close())

	require.NoError(t, applyArchiveNoSameOwner(t, dest, buf.Bytes()))

	fi, err := os.Stat(filepath.Join(dest, "file"))
	require.NoError(t, err)
	require.False(t, fi.ModTime().Before(time.Unix(0, 0)))
}

func writeTarFile(t *testing.T, tw *tar.Writer, name, content string) {
	t.Helper()

	err := tw.WriteHeader(&tar.Header{
		Name:     name,
		Typeflag: tar.TypeReg,
		Mode:     0o644,
		Size:     int64(len(content)),
	})
	require.NoError(t, err)
	_, err = tw.Write([]byte(content))
	require.NoError(t, err)
}

func applyArchiveNoSameOwner(t *testing.T, dest string, dt []byte) error {
	t.Helper()

	return applyRootArchive(t.Context(), dest, bytes.NewReader(dt), nil, nil, true)
}
