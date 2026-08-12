package file

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"testing"

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
	parent := t.TempDir()
	dest := filepath.Join(parent, "dest")
	outside := filepath.Join(parent, "outside")
	require.NoError(t, os.Mkdir(dest, 0o755))
	require.NoError(t, os.Mkdir(outside, 0o755))

	buf := bytes.NewBuffer(nil)
	tw := tar.NewWriter(buf)
	writeTarFile(t, tw, "../outside/pwned", "pwned")
	require.NoError(t, tw.Close())

	require.Error(t, applyArchiveNoSameOwner(t, dest, buf.Bytes()))

	_, err := os.Stat(filepath.Join(outside, "pwned"))
	require.True(t, os.IsNotExist(err), "archive path escaped destination")
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
	parent := t.TempDir()
	dest := filepath.Join(parent, "dest")
	outside := filepath.Join(parent, "outside")
	require.NoError(t, os.Mkdir(dest, 0o755))
	require.NoError(t, os.Mkdir(outside, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(outside, "target"), []byte("target"), 0o644))

	buf := bytes.NewBuffer(nil)
	tw := tar.NewWriter(buf)
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name:     "hardlink",
		Typeflag: tar.TypeLink,
		Linkname: filepath.ToSlash(filepath.Join(outside, "target")),
		Mode:     0o644,
	}))
	require.NoError(t, tw.Close())

	require.Error(t, applyArchiveNoSameOwner(t, dest, buf.Bytes()))

	dt, err := os.ReadFile(filepath.Join(outside, "target"))
	require.NoError(t, err)
	require.Equal(t, "target", string(dt))
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
