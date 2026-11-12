package file

import (
	stderrors "errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/moby/buildkit/solver/pb"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestRmPathNonExistentFileAllowNotFoundFalse(t *testing.T) {
	root := t.TempDir()
	err := rmPath(root, "doesnt_exist", false)
	require.Error(t, err)
	require.True(t, errors.Is(err, os.ErrNotExist))
}

func TestRmPathNonExistentFileAllowNotFoundTrue(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, rmPath(root, "doesnt_exist", true))
}

func TestRmPathFileExists(t *testing.T) {
	root := t.TempDir()

	src := filepath.Join(root, "exists")
	file, err := os.Create(src)
	require.NoError(t, err)
	file.Close()

	require.NoError(t, rmPath(root, "exists", false))

	_, err = os.Stat(src)

	require.True(t, os.IsNotExist(err))
}

func TestRmParentTraversalDoesNotEscapeRoot(t *testing.T) {
	// Backslash variants are separators on Windows (real traversal there) and
	// ordinary filename characters on Linux (inert, but still must not escape).
	for _, p := range []string{
		"..", "../..", "a/../..", "../victim",
		"..\\..", "a\\..\\..", "..\\victim",
	} {
		t.Run(p, func(t *testing.T) {
			parent := t.TempDir()
			root := filepath.Join(parent, "root")
			victim := filepath.Join(parent, "victim")

			require.NoError(t, os.Mkdir(root, 0o755))
			require.NoError(t, os.Mkdir(victim, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(victim, "data"), []byte("data"), 0o644))

			require.Error(t, rm(root, &pb.FileActionRm{Path: p}))

			_, err := os.Stat(victim)
			require.NoError(t, err, "rm escaped root and deleted sibling %q", victim)
		})
	}
}

func TestRmPathRemovesSymlinkItself(t *testing.T) {
	root := t.TempDir()

	target := filepath.Join(root, "target")
	link := filepath.Join(root, "link")

	require.NoError(t, os.WriteFile(target, []byte("target"), 0o644))
	require.NoError(t, os.Symlink("target", link))

	require.NoError(t, rmPath(root, "link", false))

	_, err := os.Lstat(link)
	require.True(t, os.IsNotExist(err))

	_, err = os.Stat(target)
	require.NoError(t, err)
}

func TestCopyWildcardDestination(t *testing.T) {
	for _, tc := range []struct {
		name          string
		src           string
		dest          string
		existing      string
		attemptUnpack bool
		wantErr       bool
	}{
		{name: "multiple files to new file", src: "*.txt", dest: "/test.txt", wantErr: true},
		{name: "multiple files to existing file", src: "*.txt", dest: "/test.txt", existing: "file", wantErr: true},
		{name: "multiple files with unpack to file", src: "*.txt", dest: "/test.txt", attemptUnpack: true, wantErr: true},
		{name: "multiple files to symlink to file", src: "*.txt", dest: "/test", existing: "symlink to file", wantErr: true},
		{name: "multiple files to new directory", src: "*.txt", dest: "/test/"},
		{name: "multiple files to directory dot", src: "*.txt", dest: "/test/."},
		{name: "multiple files to root", src: "*.txt", dest: "/"},
		{name: "multiple files to nested directory", src: "*.txt", dest: "/parent/test/"},
		{name: "multiple files to existing directory", src: "*.txt", dest: "/test", existing: "directory"},
		{name: "multiple files to symlink to directory", src: "*.txt", dest: "/test", existing: "symlink to directory"},
		{name: "single wildcard match to file", src: "test1.*", dest: "/test.txt"},
		{name: "single source to file", src: "test1.txt", dest: "/test.txt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src, dest := t.TempDir(), t.TempDir()
			for _, name := range []string{"test1.txt", "test2.txt"} {
				require.NoError(t, os.WriteFile(filepath.Join(src, name), []byte(name), 0o644))
			}
			p := filepath.Join(dest, filepath.FromSlash(tc.dest))
			switch tc.existing {
			case "file":
				require.NoError(t, os.WriteFile(p, []byte("existing"), 0o644))
			case "directory":
				require.NoError(t, os.MkdirAll(p, 0o755))
			case "symlink to file":
				require.NoError(t, os.WriteFile(filepath.Join(dest, "target"), []byte("existing"), 0o644))
				require.NoError(t, os.Symlink("target", p))
			case "symlink to directory":
				require.NoError(t, os.Mkdir(filepath.Join(dest, "target"), 0o755))
				require.NoError(t, os.Symlink("target", p))
			}

			err := docopy(t.Context(), src, dest, &pb.FileActionCopy{
				Src:                              tc.src,
				Dest:                             tc.dest,
				Mode:                             -1,
				Timestamp:                        -1,
				AllowWildcard:                    true,
				FollowSymlink:                    true,
				DirCopyContents:                  true,
				CreateDestPath:                   true,
				AttemptUnpackDockerCompatibility: tc.attemptUnpack,
			}, nil, nil)
			if tc.wantErr {
				require.ErrorContains(t, err, "destination must be a directory")
				return
			}
			require.NoError(t, err)
			fi, err := os.Stat(p)
			require.NoError(t, err)
			if fi.IsDir() {
				for _, name := range []string{"test1.txt", "test2.txt"} {
					dt, err := os.ReadFile(filepath.Join(p, name))
					require.NoError(t, err)
					require.Equal(t, name, string(dt))
				}
			} else {
				dt, err := os.ReadFile(p)
				require.NoError(t, err)
				require.Equal(t, "test1.txt", string(dt))
			}
		})
	}
}

func TestCopyWildcardDirectories(t *testing.T) {
	src, dest := t.TempDir(), t.TempDir()
	for _, name := range []string{"dir1", "dir2"} {
		require.NoError(t, os.Mkdir(filepath.Join(src, name), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(src, name, name), []byte(name), 0o644))
	}

	err := docopy(t.Context(), src, dest, &pb.FileActionCopy{
		Src:             "dir*",
		Dest:            "/merged",
		Mode:            -1,
		Timestamp:       -1,
		AllowWildcard:   true,
		FollowSymlink:   true,
		DirCopyContents: true,
		CreateDestPath:  true,
	}, nil, nil)
	require.NoError(t, err)
	for _, name := range []string{"dir1", "dir2"} {
		dt, err := os.ReadFile(filepath.Join(dest, "merged", name))
		require.NoError(t, err)
		require.Equal(t, name, string(dt))
	}
}

func TestArchivePathNeedsXz(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PATH", root)

	src := filepath.Join(root, "archive.tar.xz")
	require.NoError(t, os.WriteFile(src, []byte{0xfd, 0x37, 0x7a, 0x58, 0x5a, 0x00}, 0o600))

	ok, err := isArchivePath(src)
	require.Error(t, err)
	_, isExecErr := stderrors.AsType[*exec.Error](err)
	require.True(t, isExecErr)
	require.False(t, ok)
}
