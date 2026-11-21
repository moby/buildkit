package file

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/moby/patternmatcher"
	"github.com/stretchr/testify/require"
	copy "github.com/tonistiigi/fsutil/copy"
)

func identityChowner(u *copy.User) (*copy.User, error) {
	return u, nil
}

// writeTestArchive creates a gzipped tar with the given entries
// (name -> contents; a trailing "/" marks a directory entry).
func writeTestArchive(t *testing.T, path string, entries map[string]string) {
	t.Helper()

	f, err := os.Create(path)
	require.NoError(t, err)
	defer f.Close()

	gz := gzip.NewWriter(f)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()

	for name, contents := range entries {
		hdr := &tar.Header{
			Name: name,
			Mode: 0o644,
		}
		if len(name) > 0 && name[len(name)-1] == '/' {
			hdr.Typeflag = tar.TypeDir
			hdr.Mode = 0o755
		} else {
			hdr.Typeflag = tar.TypeReg
			hdr.Size = int64(len(contents))
		}
		require.NoError(t, tw.WriteHeader(hdr))
		if hdr.Typeflag == tar.TypeReg {
			_, err = tw.Write([]byte(contents))
			require.NoError(t, err)
		}
	}
}

func TestUnpackExcludesPatterns(t *testing.T) {
	root := t.TempDir()
	archivePath := filepath.Join(root, "go.tar.gz")
	writeTestArchive(t, archivePath, map[string]string{
		"go/":                 "",
		"go/doc/":             "",
		"go/doc/a.txt":        "doc",
		"go/doc/nested/b.txt": "nested doc",
		"go/test/":            "",
		"go/test/c.txt":       "test",
		"go/src/":             "",
		"go/src/d.txt":        "src",
		"./go/src/e.txt":      "dot-slash src",
		"README.md":           "readme",
	})

	destRoot := t.TempDir()
	ok, err := unpack(root, "go.tar.gz", destRoot, "out", identityChowner, nil, nil, nil, []string{"go/doc/**", "go/test/**"})
	require.NoError(t, err)
	require.True(t, ok)

	out := filepath.Join(destRoot, "out")
	for _, gone := range []string{"go/doc/a.txt", "go/doc/nested/b.txt", "go/test/c.txt"} {
		_, err := os.Stat(filepath.Join(out, gone))
		require.True(t, os.IsNotExist(err), "%s should have been excluded", gone)
	}
	for _, kept := range []string{"go/src/d.txt", "go/src/e.txt", "README.md"} {
		data, err := os.ReadFile(filepath.Join(out, kept))
		require.NoError(t, err, "%s should have been kept", kept)
		require.NotEmpty(t, data)
	}
}

func TestUnpackExcludesBareDirectory(t *testing.T) {
	root := t.TempDir()
	archivePath := filepath.Join(root, "go.tar.gz")
	writeTestArchive(t, archivePath, map[string]string{
		"go/test/c.txt": "test",
		"go/src/d.txt":  "src",
	})

	destRoot := t.TempDir()
	ok, err := unpack(root, "go.tar.gz", destRoot, "out", identityChowner, nil, nil, nil, []string{"go/test"})
	require.NoError(t, err)
	require.True(t, ok)

	out := filepath.Join(destRoot, "out")
	_, err = os.Stat(filepath.Join(out, "go/test/c.txt"))
	require.True(t, os.IsNotExist(err))
	require.FileExists(t, filepath.Join(out, "go/src/d.txt"))
}

func TestUnpackWithoutExcludesKeepsEverything(t *testing.T) {
	root := t.TempDir()
	archivePath := filepath.Join(root, "go.tar.gz")
	writeTestArchive(t, archivePath, map[string]string{
		"go/doc/a.txt":  "doc",
		"go/test/c.txt": "test",
		"go/src/d.txt":  "src",
	})

	destRoot := t.TempDir()
	ok, err := unpack(root, "go.tar.gz", destRoot, "out", identityChowner, nil, nil, nil, nil)
	require.NoError(t, err)
	require.True(t, ok)

	out := filepath.Join(destRoot, "out")
	for _, kept := range []string{"go/doc/a.txt", "go/test/c.txt", "go/src/d.txt"} {
		require.FileExists(t, filepath.Join(out, kept))
	}
}

func TestUnpackNonArchiveReturnsFalse(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "plain.txt"), []byte("not an archive"), 0o644))

	destRoot := t.TempDir()
	ok, err := unpack(root, "plain.txt", destRoot, "out", identityChowner, nil, nil, nil, []string{"**"})
	require.NoError(t, err)
	require.False(t, ok)
}

func TestFilterTarSkipsExcludedEntries(t *testing.T) {
	matcher, err := patternmatcher.New([]string{"go/doc/**"})
	require.NoError(t, err)

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, name := range []string{"go/doc/a.txt", "go/src/d.txt"} {
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: 1}))
		_, err = tw.Write([]byte("x"))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())

	gzr, err := gzip.NewReader(&buf)
	require.NoError(t, err)
	defer gzr.Close()

	filtered, err := io.ReadAll(filterTar(gzr, matcher))
	require.NoError(t, err)

	tr := tar.NewReader(bytes.NewReader(filtered))
	var names []string
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		names = append(names, hdr.Name)
	}
	require.Equal(t, []string{"go/src/d.txt"}, names)
}
