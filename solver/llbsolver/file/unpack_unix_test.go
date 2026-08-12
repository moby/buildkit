//go:build !windows

package file

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/containerd/containerd/v2/pkg/archive"
	"github.com/stretchr/testify/require"
)

func TestUnpackPreservesWhiteoutFiles(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("archive apply preserves tar ownership and requires root")
	}

	srcRoot := t.TempDir()
	destRoot := t.TempDir()

	buf := bytes.NewBuffer(nil)
	tw := tar.NewWriter(buf)
	writeTarFile(t, tw, "victim", "victim")
	writeTarFile(t, tw, ".wh.victim", "whiteout")
	require.NoError(t, tw.Close())

	require.NoError(t, os.WriteFile(filepath.Join(srcRoot, "archive.tar"), buf.Bytes(), 0o600))

	ok, err := unpack(t.Context(), srcRoot, "archive.tar", destRoot, "/", nil, nil, nil, nil)
	require.NoError(t, err)
	require.True(t, ok)

	dt, err := os.ReadFile(filepath.Join(destRoot, "victim"))
	require.NoError(t, err)
	require.Equal(t, "victim", string(dt))

	dt, err = os.ReadFile(filepath.Join(destRoot, ".wh.victim"))
	require.NoError(t, err)
	require.Equal(t, "whiteout", string(dt))
}

func TestApplyDoesNotWriteThroughArchiveSymlink(t *testing.T) {
	parent := t.TempDir()
	dest := filepath.Join(parent, "dest")
	outside := filepath.Join(parent, "outside")
	require.NoError(t, os.Mkdir(dest, 0o755))
	require.NoError(t, os.Mkdir(outside, 0o755))

	buf := bytes.NewBuffer(nil)
	tw := tar.NewWriter(buf)
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name:     "escape",
		Typeflag: tar.TypeSymlink,
		Linkname: filepath.ToSlash(outside),
		Mode:     0o777,
	}))
	writeTarFile(t, tw, "escape/pwned", "pwned")
	require.NoError(t, tw.Close())

	require.NoError(t, applyArchiveNoSameOwner(t, dest, buf.Bytes()))

	_, err := os.Stat(filepath.Join(outside, "pwned"))
	require.True(t, os.IsNotExist(err), "archive-created symlink escaped destination")
}

func TestApplyDoesNotWriteThroughPreexistingSymlink(t *testing.T) {
	parent := t.TempDir()
	dest := filepath.Join(parent, "dest")
	outside := filepath.Join(parent, "outside")
	require.NoError(t, os.Mkdir(dest, 0o755))
	require.NoError(t, os.Mkdir(outside, 0o755))
	require.NoError(t, os.Symlink(outside, filepath.Join(dest, "escape")))

	buf := bytes.NewBuffer(nil)
	tw := tar.NewWriter(buf)
	writeTarFile(t, tw, "escape/pwned", "pwned")
	require.NoError(t, tw.Close())

	require.NoError(t, applyArchiveNoSameOwner(t, dest, buf.Bytes()))

	_, err := os.Stat(filepath.Join(outside, "pwned"))
	require.True(t, os.IsNotExist(err), "preexisting symlink escaped destination")
}

func TestApplyDoesNotHardlinkOutsideDestination(t *testing.T) {
	parent := t.TempDir()
	dest := filepath.Join(parent, "dest")
	outside := filepath.Join(parent, "outside")
	require.NoError(t, os.Mkdir(dest, 0o755))
	require.NoError(t, os.Mkdir(outside, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(outside, "target"), []byte("target"), 0o644))

	buf := bytes.NewBuffer(nil)
	tw := tar.NewWriter(buf)
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name:     "hardlink",
		Typeflag: tar.TypeLink,
		Linkname: filepath.ToSlash(filepath.Join(outside, "target")),
		Mode:     0o644,
	}))
	require.NoError(t, tw.Close())

	require.Error(t, applyArchiveNoSameOwner(t, dest, buf.Bytes()))

	dt, err := os.ReadFile(filepath.Join(outside, "target"))
	require.NoError(t, err)
	require.Equal(t, "target", string(dt))
}

func applyArchiveNoSameOwner(t *testing.T, dest string, dt []byte) error {
	t.Helper()

	_, err := archive.Apply(t.Context(), dest, bytes.NewReader(dt),
		archive.WithConvertWhiteout(func(_ *tar.Header, _ string) (bool, error) {
			return true, nil
		}),
		archive.WithNoSameOwner(),
	)
	return err
}
