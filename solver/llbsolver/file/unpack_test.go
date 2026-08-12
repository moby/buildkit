package file

import (
	"archive/tar"
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

	ok, err := ownerMapper(nil, idmap)(hdr)
	require.NoError(t, err)
	require.True(t, ok)
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

	ok, err := ownerMapper(&copy.User{UID: 5, GID: 6}, idmap)(hdr)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, 5, hdr.Uid)
	require.Equal(t, 6, hdr.Gid)
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
