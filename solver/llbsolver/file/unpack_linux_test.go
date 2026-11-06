package file

import (
	"archive/tar"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/moby/buildkit/solver/pb"
	"github.com/stretchr/testify/require"
)

func TestUnpackDestTimestamp(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("unpacking requires root")
	}
	src := t.TempDir()
	f, err := os.Create(filepath.Join(src, "a.tar"))
	require.NoError(t, err)
	tw := tar.NewWriter(f)
	require.NoError(t, tw.WriteHeader(&tar.Header{Name: "foo", Mode: 0644, Typeflag: tar.TypeReg, ModTime: time.Unix(1000, 0)}))
	require.NoError(t, tw.Close())
	require.NoError(t, f.Close())

	dest := t.TempDir()
	tm := time.Unix(42, 0)
	require.NoError(t, docopy(t.Context(), src, dest, &pb.FileActionCopy{
		Src:                              "/a.tar",
		Dest:                             "/x/y/",
		Mode:                             -1,
		CreateDestPath:                   true,
		AttemptUnpackDockerCompatibility: true,
		Timestamp:                        tm.UnixNano(),
	}, nil, nil))

	for _, p := range []string{"x", "x/y"} {
		fi, err := os.Stat(filepath.Join(dest, p))
		require.NoError(t, err)
		require.Equal(t, tm.Unix(), fi.ModTime().Unix(), "mtime of %s", p)
	}
	fi, err := os.Stat(filepath.Join(dest, "x/y/foo"))
	require.NoError(t, err)
	require.Equal(t, int64(1000), fi.ModTime().Unix())
}
