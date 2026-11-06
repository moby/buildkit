//go:build !windows

package file

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/moby/buildkit/solver/pb"
	"github.com/stretchr/testify/require"
)

func TestCopyTimestampOnlyCreatedDirs(t *testing.T) {
	src := t.TempDir()
	srcTime := time.Unix(1000, 0)
	require.NoError(t, os.WriteFile(filepath.Join(src, "foo"), []byte("foo"), 0644))
	require.NoError(t, os.Chtimes(filepath.Join(src, "foo"), srcTime, srcTime))

	tm := time.Unix(42, 0)
	for _, tc := range []struct {
		name        string
		onlyDirs    bool
		expectedTop time.Time
	}{
		{name: "only created dirs", onlyDirs: true, expectedTop: srcTime},
		{name: "all", onlyDirs: false, expectedTop: tm},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dest := t.TempDir()
			require.NoError(t, docopy(t.Context(), src, dest, &pb.FileActionCopy{
				Src:                      "/foo",
				Dest:                     "/a/b/c/",
				Mode:                     -1,
				CreateDestPath:           true,
				Timestamp:                tm.UnixNano(),
				TimestampOnlyCreatedDirs: tc.onlyDirs,
			}, nil, nil))
			requireMtime(t, tm, filepath.Join(dest, "a"), filepath.Join(dest, "a/b"), filepath.Join(dest, "a/b/c"))
			requireMtime(t, tc.expectedTop, filepath.Join(dest, "a/b/c/foo"))
		})
	}
}

func TestArchiveImplicitDirs(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "c-1.0.crate")
	f, err := os.Create(archive)
	require.NoError(t, err)
	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)
	for _, h := range []tar.Header{
		{Name: "c-1.0/Cargo.toml", Typeflag: tar.TypeReg},
		{Name: "c-1.0/src/lib.rs", Typeflag: tar.TypeReg},
		{Name: "./c-1.0/explicit/", Typeflag: tar.TypeDir, Mode: 0755},
		{Name: "c-1.0/explicit/sub/f", Typeflag: tar.TypeReg},
		{Name: "existing/f", Typeflag: tar.TypeReg},
		{Name: "../escape/f", Typeflag: tar.TypeReg},
	} {
		h.ModTime = time.Unix(1000, 0)
		if h.Mode == 0 {
			h.Mode = 0644
		}
		require.NoError(t, tw.WriteHeader(&h))
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gw.Close())
	require.NoError(t, f.Close())

	dest := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dest, "existing"), 0755))

	dirs, err := archiveImplicitDirs(archive, dest)
	require.NoError(t, err)
	slices.Sort(dirs)
	require.Equal(t, []string{
		filepath.Join(dest, "c-1.0"),
		filepath.Join(dest, "c-1.0/explicit/sub"),
		filepath.Join(dest, "c-1.0/src"),
		filepath.Join(dest, "escape"),
	}, dirs)
}
