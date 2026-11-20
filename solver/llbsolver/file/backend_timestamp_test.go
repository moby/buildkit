//go:build !windows

package file

import (
	"archive/tar"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/moby/buildkit/solver/pb"
	"github.com/moby/sys/reexec"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	copy "github.com/tonistiigi/fsutil/copy"
)

func TestMain(m *testing.M) {
	if reexec.Init() {
		return
	}
	os.Exit(m.Run())
}

func TestExplicitTimestampPreservesExistingParentModTime(t *testing.T) {
	parentEpoch := time.Unix(1000, 0)
	childEpoch := time.Unix(42, 0)

	t.Run("mkdir MakeParents=true", func(t *testing.T) {
		root := t.TempDir()
		require.NoError(t, mkdir(root, &pb.FileActionMkDir{
			Path:        "/monorepo",
			Mode:        0o755,
			MakeParents: true,
			Timestamp:   parentEpoch.UnixNano(),
		}, nil, nil))

		require.NoError(t, mkdir(root, &pb.FileActionMkDir{
			Path:        "/monorepo/app",
			Mode:        0o755,
			MakeParents: true,
			Timestamp:   childEpoch.UnixNano(),
		}, nil, nil))

		fiParent, err := os.Lstat(filepath.Join(root, "monorepo"))
		require.NoError(t, err)
		require.Equal(t, parentEpoch.UnixNano(), fiParent.ModTime().UnixNano())

		fiChild, err := os.Lstat(filepath.Join(root, "monorepo/app"))
		require.NoError(t, err)
		require.Equal(t, childEpoch.UnixNano(), fiChild.ModTime().UnixNano())
	})

	t.Run("mkdir MakeParents=false", func(t *testing.T) {
		root := t.TempDir()
		existing := filepath.Join(root, "existing")
		require.NoError(t, os.Mkdir(existing, 0o755))
		require.NoError(t, os.Chtimes(existing, parentEpoch, parentEpoch))

		require.NoError(t, mkdir(root, &pb.FileActionMkDir{
			Path:      "/existing/sub",
			Mode:      0o755,
			Timestamp: childEpoch.UnixNano(),
		}, nil, nil))

		fiParent, err := os.Lstat(existing)
		require.NoError(t, err)
		require.Equal(t, parentEpoch.UnixNano(), fiParent.ModTime().UnixNano())
	})

	t.Run("mkfile", func(t *testing.T) {
		root := t.TempDir()
		existing := filepath.Join(root, "existing")
		require.NoError(t, os.Mkdir(existing, 0o755))
		require.NoError(t, os.Chtimes(existing, parentEpoch, parentEpoch))

		require.NoError(t, mkfile(root, &pb.FileActionMkFile{
			Path:      "/existing/hello.txt",
			Mode:      0o644,
			Data:      []byte("hi"),
			Timestamp: childEpoch.UnixNano(),
		}, nil, nil))

		fiParent, err := os.Lstat(existing)
		require.NoError(t, err)
		require.Equal(t, parentEpoch.UnixNano(), fiParent.ModTime().UnixNano())

		fiFile, err := os.Lstat(filepath.Join(existing, "hello.txt"))
		require.NoError(t, err)
		require.Equal(t, childEpoch.UnixNano(), fiFile.ModTime().UnixNano())
	})

	t.Run("symlink", func(t *testing.T) {
		root := t.TempDir()
		existing := filepath.Join(root, "existing")
		require.NoError(t, os.Mkdir(existing, 0o755))
		require.NoError(t, os.Chtimes(existing, parentEpoch, parentEpoch))

		require.NoError(t, symlink(root, &pb.FileActionSymlink{
			Oldpath:   "target",
			Newpath:   "/existing/link",
			Timestamp: childEpoch.UnixNano(),
		}, nil, nil))

		fiParent, err := os.Lstat(existing)
		require.NoError(t, err)
		require.Equal(t, parentEpoch.UnixNano(), fiParent.ModTime().UnixNano())

		fiLink, err := os.Lstat(filepath.Join(existing, "link"))
		require.NoError(t, err)
		require.Equal(t, childEpoch.UnixNano(), fiLink.ModTime().UnixNano())
	})

	t.Run("docopy into existing directory", func(t *testing.T) {
		src := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(src, "f.txt"), []byte("x"), 0o644))

		dest := t.TempDir()
		existing := filepath.Join(dest, "monorepo")
		require.NoError(t, os.Mkdir(existing, 0o755))
		require.NoError(t, os.Chtimes(existing, parentEpoch, parentEpoch))

		require.NoError(t, docopy(t.Context(), src, dest, &pb.FileActionCopy{
			Src:            "/f.txt",
			Dest:           "/monorepo/",
			Mode:           -1,
			CreateDestPath: true,
			Timestamp:      childEpoch.UnixNano(),
		}, nil, nil))

		fiParent, err := os.Lstat(existing)
		require.NoError(t, err)
		require.Equal(t, parentEpoch.UnixNano(), fiParent.ModTime().UnixNano())

		fiFile, err := os.Lstat(filepath.Join(existing, "f.txt"))
		require.NoError(t, err)
		require.Equal(t, childEpoch.UnixNano(), fiFile.ModTime().UnixNano())
	})

	t.Run("unset timestamp (-1) leaves wall-clock behavior unchanged", func(t *testing.T) {
		root := t.TempDir()
		existing := filepath.Join(root, "existing")
		require.NoError(t, os.Mkdir(existing, 0o755))
		require.NoError(t, os.Chtimes(existing, parentEpoch, parentEpoch))

		require.NoError(t, mkfile(root, &pb.FileActionMkFile{
			Path:      "/existing/hello.txt",
			Mode:      0o644,
			Data:      []byte("hi"),
			Timestamp: -1,
		}, nil, nil))

		fiParent, err := os.Lstat(existing)
		require.NoError(t, err)
		require.Greater(t, fiParent.ModTime().Unix(), parentEpoch.Unix())
	})

	t.Run("snapshot root mtime and distinct parent atime preserved", func(t *testing.T) {
		root := t.TempDir()
		parentAtime := time.Unix(700, 0)
		require.NoError(t, os.Chtimes(root, parentAtime, parentEpoch))
		preFi, err := os.Lstat(root)
		require.NoError(t, err)
		preAtime := copy.StatAtime(preFi.Sys().(*syscall.Stat_t))

		require.NoError(t, mkfile(root, &pb.FileActionMkFile{
			Path:      "/rootfile.txt",
			Mode:      0o644,
			Data:      []byte("root"),
			Timestamp: childEpoch.UnixNano(),
		}, nil, nil))

		fiRoot, err := os.Lstat(root)
		require.NoError(t, err)
		require.Equal(t, parentEpoch.UnixNano(), fiRoot.ModTime().UnixNano())
		gotAtime := copy.StatAtime(fiRoot.Sys().(*syscall.Stat_t))
		require.NotEqual(t, parentEpoch.Unix(), int64(gotAtime.Sec))
		if int64(preAtime.Sec) == parentAtime.Unix() {
			require.Equal(t, parentAtime.Unix(), int64(gotAtime.Sec))
		}
	})

	t.Run("docopy AlwaysReplaceExistingDestPaths preserves existing destination", func(t *testing.T) {
		src := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(src, "target"), []byte("x"), 0o644))

		dest := t.TempDir()
		existingParent := filepath.Join(dest, "existing")
		require.NoError(t, os.MkdirAll(filepath.Join(existingParent, "target"), 0o755))
		require.NoError(t, os.Chtimes(existingParent, parentEpoch, parentEpoch))

		require.NoError(t, docopy(t.Context(), src, dest, &pb.FileActionCopy{
			Src:                            "/",
			Dest:                           "/existing",
			Mode:                           -1,
			DirCopyContents:                true,
			CreateDestPath:                 true,
			AlwaysReplaceExistingDestPaths: true,
			Timestamp:                      childEpoch.UnixNano(),
		}, nil, nil))

		fiParent, err := os.Lstat(existingParent)
		require.NoError(t, err)
		require.Equal(t, parentEpoch.UnixNano(), fiParent.ModTime().UnixNano())

		fiTarget, err := os.Lstat(filepath.Join(existingParent, "target"))
		require.NoError(t, err)
		require.False(t, fiTarget.IsDir())
		require.Equal(t, childEpoch.UnixNano(), fiTarget.ModTime().UnixNano())
	})

	t.Run("unpack creating new destination preserves enclosing parent mtime", func(t *testing.T) {
		if os.Geteuid() != 0 {
			t.Skip("chrootarchive.Untar requires root")
		}
		src := t.TempDir()
		tarPath := filepath.Join(src, "bundle.tar")
		tf, err := os.Create(tarPath)
		require.NoError(t, err)
		tw := tar.NewWriter(tf)
		body := []byte("archived")
		require.NoError(t, tw.WriteHeader(&tar.Header{
			Name:     "file.txt",
			Typeflag: tar.TypeReg,
			Mode:     0o644,
			Size:     int64(len(body)),
			ModTime:  childEpoch,
		}))
		_, err = tw.Write(body)
		require.NoError(t, err)
		require.NoError(t, tw.Close())
		require.NoError(t, tf.Close())

		dest := t.TempDir()
		monorepo := filepath.Join(dest, "monorepo")
		require.NoError(t, os.MkdirAll(monorepo, 0o755))
		require.NoError(t, os.Chtimes(monorepo, parentEpoch, parentEpoch))

		if err := docopy(t.Context(), src, dest, &pb.FileActionCopy{
			Src:                              "/bundle.tar",
			Dest:                             "/monorepo/unpacked/",
			Mode:                             -1,
			CreateDestPath:                   true,
			AttemptUnpackDockerCompatibility: true,
			Timestamp:                        childEpoch.UnixNano(),
		}, nil, nil); err != nil {
			if errors.Is(err, syscall.EPERM) {
				t.Skipf("chrootarchive.Untar requires mount namespace privileges: %v", err)
			}
			require.NoError(t, err)
		}

		fi, err := os.Lstat(monorepo)
		require.NoError(t, err)
		require.Equal(t, parentEpoch.UnixNano(), fi.ModTime().UnixNano())
	})
}
