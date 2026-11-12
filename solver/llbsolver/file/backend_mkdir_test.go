//go:build !windows

package file

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/moby/buildkit/solver/pb"
	"github.com/stretchr/testify/require"
)

func requireMtime(t *testing.T, tm time.Time, paths ...string) {
	t.Helper()
	for _, p := range paths {
		fi, err := os.Lstat(p)
		require.NoError(t, err)
		require.Equal(t, tm.UnixNano(), fi.ModTime().UnixNano(), "mtime of %s", p)
	}
}

func TestMkdirMakeParentsTimestamp(t *testing.T) {
	root := t.TempDir()
	tm := time.Unix(42, 0)

	require.NoError(t, mkdir(root, &pb.FileActionMkDir{
		Path:        "/target/release/deps",
		Mode:        0755,
		MakeParents: true,
		Timestamp:   tm.UnixNano(),
	}, nil, nil))

	requireMtime(t, tm,
		filepath.Join(root, "target"),
		filepath.Join(root, "target/release"),
		filepath.Join(root, "target/release/deps"),
	)
}

func TestMkdirMakeParentsKeepsExistingDirs(t *testing.T) {
	root := t.TempDir()
	existing := filepath.Join(root, "target")
	require.NoError(t, os.Mkdir(existing, 0755))
	old := time.Unix(1000, 0)
	require.NoError(t, os.Chtimes(existing, old, old))

	tm := time.Unix(42, 0)
	require.NoError(t, mkdir(root, &pb.FileActionMkDir{
		Path:        "/target",
		Mode:        0755,
		MakeParents: true,
		Timestamp:   tm.UnixNano(),
	}, nil, nil))
	requireMtime(t, old, existing)
}
