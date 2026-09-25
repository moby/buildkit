//go:build linux

package file

import (
	"archive/tar"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestUnpackRestoresXattrsWithoutReadPermission(t *testing.T) {
	for _, preexisting := range []bool{false, true} {
		name := "new directory"
		if preexisting {
			name = "existing directory"
		}
		t.Run(name, func(t *testing.T) {
			dest := t.TempDir()
			const key = "user.buildkit.dir"
			if err := unix.Setxattr(dest, key, []byte("probe"), 0); err != nil {
				if isBestEffortRootXattrError(err) {
					t.Skipf("user xattrs are not supported: %v", err)
				}
				require.NoError(t, err)
			}
			dir := filepath.Join(dest, "dir")
			if preexisting {
				require.NoError(t, os.Mkdir(dir, 0o300))
			}
			t.Cleanup(func() { require.NoError(t, os.Chmod(dir, 0o700)) })
			buf := &bytes.Buffer{}
			tw := tar.NewWriter(buf)
			require.NoError(t, tw.WriteHeader(&tar.Header{
				Name:       "dir",
				Typeflag:   tar.TypeDir,
				Mode:       0o300,
				PAXRecords: map[string]string{"SCHILY.xattr." + key: "dir-value"},
			}))
			require.NoError(t, tw.Close())

			require.NoError(t, applyArchiveNoSameOwner(t, dest, buf.Bytes()))
			fi, err := os.Stat(dir)
			require.NoError(t, err)
			require.Equal(t, os.FileMode(0o300), fi.Mode().Perm())
			require.NoError(t, os.Chmod(dir, 0o700))
			value := make([]byte, 128)
			n, err := unix.Lgetxattr(dir, key, value)
			require.NoError(t, err)
			require.Equal(t, "dir-value", string(value[:n]))
		})
	}
}

func TestUnpackRestoresHardlinkXattrsWithoutReadPermission(t *testing.T) {
	dest := t.TempDir()
	source := filepath.Join(dest, "source")
	require.NoError(t, os.WriteFile(source, []byte("content"), 0o200))
	const key = "user.buildkit.file"
	if err := unix.Setxattr(source, key, []byte("before"), 0); err != nil {
		if isBestEffortRootXattrError(err) {
			t.Skipf("user xattrs are not supported: %v", err)
		}
		require.NoError(t, err)
	}
	buf := &bytes.Buffer{}
	tw := tar.NewWriter(buf)
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name:       "hardlink",
		Typeflag:   tar.TypeLink,
		Linkname:   "source",
		Mode:       0o200,
		PAXRecords: map[string]string{"SCHILY.xattr." + key: "after"},
	}))
	require.NoError(t, tw.Close())

	require.NoError(t, applyArchiveNoSameOwner(t, dest, buf.Bytes()))
	fi, err := os.Stat(source)
	require.NoError(t, err)
	hi, err := os.Stat(filepath.Join(dest, "hardlink"))
	require.NoError(t, err)
	require.True(t, os.SameFile(fi, hi))
	require.Equal(t, os.FileMode(0o200), fi.Mode().Perm())
	require.NoError(t, os.Chmod(source, 0o600))
	value := make([]byte, 128)
	n, err := unix.Lgetxattr(source, key, value)
	require.NoError(t, err)
	require.Equal(t, "after", string(value[:n]))
}

func TestRootXattrRejectsSymlinkEscape(t *testing.T) {
	base := t.TempDir()
	dest := filepath.Join(base, "dest")
	require.NoError(t, os.Mkdir(dest, 0o700))
	outside := filepath.Join(base, "outside")
	require.NoError(t, os.WriteFile(outside, []byte("content"), 0o200))
	const key = "user.buildkit.file"
	if err := unix.Setxattr(outside, key, []byte("before"), 0); err != nil {
		if isBestEffortRootXattrError(err) {
			t.Skipf("user xattrs are not supported: %v", err)
		}
		require.NoError(t, err)
	}
	require.NoError(t, os.Symlink("../outside", filepath.Join(dest, "leaf")))
	require.NoError(t, os.Symlink("..", filepath.Join(dest, "parent")))
	root, err := os.OpenRoot(dest)
	require.NoError(t, err)
	defer root.Close()
	for _, name := range []string{"leaf", "parent/outside"} {
		require.Error(t, setRootXattr(root, nil, name, key, []byte("after")))
	}
	require.NoError(t, os.Chmod(outside, 0o600))
	value := make([]byte, 128)
	n, err := unix.Lgetxattr(outside, key, value)
	require.NoError(t, err)
	require.Equal(t, "before", string(value[:n]))
}

func TestUnpackRestoresUserXattrs(t *testing.T) {
	dest := t.TempDir()
	probe := filepath.Join(dest, "probe")
	require.NoError(t, os.WriteFile(probe, []byte("probe"), 0o644))
	if err := unix.Lsetxattr(probe, "user.buildkit.probe", []byte("ok"), 0); err != nil {
		if errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.EPERM) {
			t.Skipf("user xattrs are not supported on this filesystem: %v", err)
		}
		require.NoError(t, err)
	}
	require.NoError(t, os.Remove(probe))

	buf := bytes.NewBuffer(nil)
	tw := tar.NewWriter(buf)
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name:     "dir",
		Typeflag: tar.TypeDir,
		Mode:     0o755,
		PAXRecords: map[string]string{
			"SCHILY.xattr.user.buildkit.dir": "dir-value",
		},
	}))
	content := "content"
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name:     "dir/file",
		Typeflag: tar.TypeReg,
		Mode:     0o644,
		Size:     int64(len(content)),
		PAXRecords: map[string]string{
			"SCHILY.xattr.user.buildkit.file": "file-value",
		},
	}))
	_, err := tw.Write([]byte(content))
	require.NoError(t, err)
	require.NoError(t, tw.Close())

	require.NoError(t, applyArchiveNoSameOwner(t, dest, buf.Bytes()))

	xattrValue := make([]byte, 128)
	n, err := unix.Lgetxattr(filepath.Join(dest, "dir"), "user.buildkit.dir", xattrValue)
	require.NoError(t, err)
	require.Equal(t, "dir-value", string(xattrValue[:n]))

	n, err = unix.Lgetxattr(filepath.Join(dest, "dir", "file"), "user.buildkit.file", xattrValue)
	require.NoError(t, err)
	require.Equal(t, "file-value", string(xattrValue[:n]))
}
