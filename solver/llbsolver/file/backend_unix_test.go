//go:build !windows

package file

import (
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/moby/buildkit/solver/pb"
	"github.com/stretchr/testify/require"
)

// An earlier action in the same build can leave a non-regular inode at the
// destination: the default capability set lets an ExecOp mknod a device node,
// and runc allows it for every device. mkfile must replace such an inode rather
// than open it, since opening a device node resolves it against the host device
// table and performs I/O outside the snapshot.
func requireMkfileReplaces(t *testing.T, create func(t *testing.T, p string)) {
	t.Helper()

	root := t.TempDir()
	p := filepath.Join(root, "target")
	create(t, p)

	require.NoError(t, mkfile(root, &pb.FileActionMkFile{
		Path:      "/target",
		Data:      []byte("contents"),
		Mode:      0644,
		Timestamp: -1,
	}, nil, nil))

	fi, err := os.Lstat(p)
	require.NoError(t, err)
	require.True(t, fi.Mode().IsRegular(), "expected regular file, got %s", fi.Mode())
	require.Equal(t, os.FileMode(0644), fi.Mode().Perm())

	dt, err := os.ReadFile(p)
	require.NoError(t, err)
	require.Equal(t, []byte("contents"), dt)

	requireOnlyTarget(t, root)
}

func TestMkfileReplacesSocket(t *testing.T) {
	requireMkfileReplaces(t, func(t *testing.T, p string) {
		var lc net.ListenConfig
		l, err := lc.Listen(t.Context(), "unix", p)
		require.NoError(t, err)
		t.Cleanup(func() { l.Close() })
	})
}

// A directory is not silently replaced by a file, and the failure does not
// disclose the mount point or the temporary file used to stage the write.
func TestMkfileDirectoryTargetKeepsPathsInternal(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "target")
	require.NoError(t, os.Mkdir(p, 0755))

	err := mkfile(root, &pb.FileActionMkFile{
		Path:      "/target",
		Data:      []byte("contents"),
		Mode:      0644,
		Timestamp: -1,
	}, nil, nil)
	require.Error(t, err)

	// rename reports *os.LinkError, which names both the staged file and the
	// destination
	requirePathsInternal(t, err, root, "/target")

	fi, err := os.Lstat(p)
	require.NoError(t, err)
	require.True(t, fi.IsDir())

	requireOnlyTarget(t, root)
}

// A failure before the staged file is renamed into place must report the
// destination the caller asked for, not the staged file.
func TestMkfileMissingParentKeepsPathsInternal(t *testing.T) {
	root := t.TempDir()

	err := mkfile(root, &pb.FileActionMkFile{
		Path:      "/nope/target",
		Data:      []byte("contents"),
		Mode:      0644,
		Timestamp: -1,
	}, nil, nil)
	require.Error(t, err)

	requirePathsInternal(t, err, root, "/nope/target")
}

// A destination resolving to the mount root has no parent inside the snapshot,
// so it must be rejected before anything is staged next to the mount.
func TestMkfileRootTargetStagesNothingOutside(t *testing.T) {
	for _, p := range []string{"/", "", ".", "/.."} {
		t.Run(p, func(t *testing.T) {
			parent := t.TempDir()
			root := filepath.Join(parent, "mount")
			require.NoError(t, os.Mkdir(root, 0755))

			err := mkfile(root, &pb.FileActionMkFile{
				Path:      p,
				Data:      []byte("contents"),
				Mode:      0644,
				Timestamp: -1,
			}, nil, nil)
			require.Error(t, err)
			requirePathsInternal(t, err, root, "/")

			entries, err := os.ReadDir(parent)
			require.NoError(t, err)
			require.Len(t, entries, 1, "nothing may be staged beside the mount root")
			require.Equal(t, "mount", entries[0].Name())

			entries, err = os.ReadDir(root)
			require.NoError(t, err)
			require.Empty(t, entries)
		})
	}
}

// requirePathsInternal checks that a failure names the path the caller asked
// for and nothing else. Every operation behind mkfile must report an
// *os.PathError so that the staged name and the mount point can be rewritten;
// an error that only formats a path into its message cannot be.
func requirePathsInternal(t *testing.T, err error, root, want string) {
	t.Helper()
	require.NotContains(t, err.Error(), root, "mount point leaked")
	require.NotContains(t, err.Error(), ".tmp-mkfile", "staged file name leaked")
	require.Contains(t, err.Error(), want)

	var pathErr *os.PathError
	require.ErrorAs(t, err, &pathErr, "failure must carry a rewritable path")
	require.Equal(t, want, pathErr.Path)
}

func requireOnlyTarget(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	for _, e := range entries {
		require.Equal(t, "target", e.Name(), "staged file left behind")
	}
}
