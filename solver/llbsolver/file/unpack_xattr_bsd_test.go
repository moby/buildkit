//go:build freebsd || netbsd

package file

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestUnpackSkipsUnsupportedXattrNamespaces(t *testing.T) {
	for _, key := range []string{"security.capability", "trusted.overlay.opaque", "com.apple.quarantine", "user", "system", "users.buildkit", "User.buildkit"} {
		t.Run(key, func(t *testing.T) {
			dest := t.TempDir()
			var buf bytes.Buffer
			tw := tar.NewWriter(&buf)
			for _, hdr := range []*tar.Header{
				{Name: "dir", Typeflag: tar.TypeDir, Mode: 0o755},
				{Name: "dir/file", Typeflag: tar.TypeReg, Mode: 0o600, Size: 7},
				{Name: "hardlink", Typeflag: tar.TypeLink, Linkname: "dir/file", Mode: 0o600},
			} {
				hdr.PAXRecords = map[string]string{"SCHILY.xattr." + key: "value"}
				require.NoError(t, tw.WriteHeader(hdr))
				if hdr.Size != 0 {
					_, err := tw.Write([]byte("content"))
					require.NoError(t, err)
				}
			}
			require.NoError(t, tw.Close())
			require.NoError(t, applyRootArchive(t.Context(), dest, &buf, nil, nil, true))
			contents, err := os.ReadFile(filepath.Join(dest, "hardlink"))
			require.NoError(t, err)
			require.Equal(t, "content", string(contents))
		})
	}
}

func TestRootXattrSupportedNamespacesKeepErrors(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	require.NoError(t, err)
	defer root.Close()
	file, err := root.Create("file")
	require.NoError(t, err)
	// A closed descriptor proves supported namespaces reach the syscall rather than being skipped.
	require.NoError(t, file.Close())
	for _, key := range []string{"user.buildkit", "system.buildkit", "user.security.capability"} {
		t.Run(key, func(t *testing.T) {
			require.ErrorIs(t, setRootXattr(root, file, "file", key, []byte("value")), unix.EBADF)
			require.ErrorIs(t, setRootXattr(root, nil, "missing", key, []byte("value")), os.ErrNotExist)
		})
	}
	for _, err := range []error{unix.ENOATTR, unix.EINVAL, unix.EBADF} {
		require.False(t, isBestEffortRootXattrError(err))
	}
	require.ErrorIs(t, setRootXattr(root, file, "file", "user.bad\x00name", []byte("value")), unix.EINVAL)
}

func TestUnpackRestoresBSDUserXattr(t *testing.T) {
	dest := t.TempDir()
	const key = "user.buildkit"
	if err := unix.Setxattr(dest, key, []byte("probe"), 0); err != nil {
		if isBestEffortRootXattrError(err) {
			t.Skipf("user xattrs are not supported: %v", err)
		}
		require.NoError(t, err)
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, hdr := range []*tar.Header{
		{Name: "dir", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "file", Typeflag: tar.TypeReg, Mode: 0o600},
	} {
		hdr.PAXRecords = map[string]string{"SCHILY.xattr." + key: "restored"}
		require.NoError(t, tw.WriteHeader(hdr))
	}
	require.NoError(t, tw.Close())
	require.NoError(t, applyRootArchive(t.Context(), dest, &buf, nil, nil, true))
	for _, name := range []string{"dir", "file"} {
		value := make([]byte, 32)
		n, err := unix.Getxattr(filepath.Join(dest, name), key, value)
		require.NoError(t, err)
		require.Equal(t, "restored", string(value[:n]))
	}
}
