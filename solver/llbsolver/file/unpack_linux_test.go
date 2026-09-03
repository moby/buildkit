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
