package local

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"testing"
	"time"

	"github.com/containerd/containerd/v2/core/mount"
	"github.com/containerd/continuity/fs/fstest"
	"github.com/moby/buildkit/cache"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/snapshot"
	"github.com/moby/sys/user"
	"github.com/stretchr/testify/require"
	"github.com/tonistiigi/fsutil"
)

func TestCreateFSOptsLoadSource(t *testing.T) {
	var opts CreateFSOpts
	rest, err := opts.Load(map[string]string{keySource: "   "})
	require.NoError(t, err)
	require.Equal(t, "   ", opts.Source)
	require.NotContains(t, rest, keySource)

	_, err = opts.Load(map[string]string{keySource: ""})
	require.ErrorContains(t, err, "empty value for src")
}

// builds a container filesystem
// -> is a symlink same output that tree command would produce
//
//	.
//	├── app -> /etc
//	├── etc
//	│   └── inside.txt
//	├── rel -> sub
//	├── sub
//	│   └── nested.txt
//	└── top.txt
func newRootfs(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	require.NoError(t, fstest.Apply(
		fstest.CreateFile("top.txt", []byte("top"), 0600),
		fstest.CreateDir("sub", 0700),
		fstest.CreateFile("sub/nested.txt", []byte("nested"), 0600),
		fstest.CreateDir("etc", 0700),
		fstest.CreateFile("etc/inside.txt", []byte("inside"), 0600),
		fstest.Symlink("/etc", "app"),
		fstest.Symlink("sub", "rel"),
	).Apply(root))

	return root
}

func walkNames(t *testing.T, f fsutil.FS) []string {
	t.Helper()

	var names []string
	require.NoError(t, f.Walk(t.Context(), "", func(p string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		names = append(names, filepath.ToSlash(p))
		return nil
	}))
	sort.Strings(names)
	return names
}

func TestResolveSafeSource(t *testing.T) {
	tests := []struct {
		name    string
		source  string
		want    []string
		wantErr string
		wantIs  error
	}{
		{
			name:   "empty source exports the whole mount",
			source: "",
			want:   []string{"app", "etc", "etc/inside.txt", "rel", "sub", "sub/nested.txt", "top.txt"},
		},
		{
			name:   "relative source is anchored to mount",
			source: "sub",
			want:   []string{"nested.txt"},
		},
		{
			name:   "subdirectory is re-rooted",
			source: "/sub",
			want:   []string{"nested.txt"},
		},
		{
			source: "/app",
			name:   "absolute symlink cannot escape the mount",
			want:   []string{"inside.txt"},
		},
		{
			name:   "relative symlink resolves inside the mount",
			source: "/rel",
			want:   []string{"nested.txt"},
		},
		{
			// Parent traversal cannot escape the mounted result.
			name:   "parent traversal is clamped to the mount",
			source: "../../etc",
			want:   []string{"inside.txt"},
		},
		{
			name:    "missing path fails",
			source:  "/nope",
			wantErr: "src=/nope:",
			wantIs:  os.ErrNotExist,
		},
		{
			name:    "file is not a directory",
			source:  "/top.txt",
			wantErr: "src=/top.txt:",
			wantIs:  syscall.ENOTDIR,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			outputFS, err := resolveSafeSource(newRootfs(t), tc.source)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				require.ErrorIs(t, err, tc.wantIs)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, walkNames(t, outputFS))
		})
	}
}

func TestSourceResolvesParentAfterSymlink(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, fstest.Apply(
		fstest.CreateFile("top.txt", []byte("top"), 0600),
		fstest.CreateDir("sub", 0755),
		fstest.CreateFile("sub/nested.txt", []byte("nested"), 0600),
		fstest.CreateDir("sub/inner", 0755),
		fstest.Symlink("/sub/inner", "link"),
	).Apply(root))

	var opts CreateFSOpts
	_, err := opts.Load(map[string]string{keySource: "/link/.."})
	require.NoError(t, err)

	outputFS, err := resolveSafeSource(root, opts.Source)
	require.NoError(t, err)
	require.Equal(t, []string{"inner", "nested.txt"}, walkNames(t, outputFS))
}

func TestCreateFSReleasesTempDirOnSourceError(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("TMPDIR", tempDir)
	t.Setenv("TMP", tempDir)
	t.Setenv("TEMP", tempDir)
	require.Equal(t, tempDir, os.TempDir())

	_, cleanup, err := CreateFS(t.Context(), "", "", nil, nil, time.Time{}, false, CreateFSOpts{Source: "/missing"})
	require.ErrorIs(t, err, os.ErrNotExist)
	require.Nil(t, cleanup)

	entries, err := os.ReadDir(tempDir)
	require.NoError(t, err)
	require.Empty(t, entries)
}

type sourceTestRef struct {
	// CreateFS only calls Mount; the remaining methods are not needed here.
	cache.ImmutableRef
	mount snapshot.Mountable
}

func (r sourceTestRef) Mount(context.Context, bool, session.Group) (snapshot.Mountable, error) {
	return r.mount, nil
}

type sourceTestMountable struct {
	root     string
	releases int
}

func (m *sourceTestMountable) Mount() ([]mount.Mount, func() error, error) {
	return []mount.Mount{{Type: "bind", Source: m.root}}, func() error {
		m.releases++
		return nil
	}, nil
}

func (m *sourceTestMountable) IdentityMapping() *user.IdentityMapping {
	return nil
}

func TestCreateFSReleasesMountOnSourceError(t *testing.T) {
	m := &sourceTestMountable{root: t.TempDir()}
	ref := sourceTestRef{mount: m}

	_, cleanup, err := CreateFS(t.Context(), "", "", ref, nil, time.Time{}, false, CreateFSOpts{Source: "/missing"})
	require.ErrorIs(t, err, os.ErrNotExist)
	require.Nil(t, cleanup)
	require.Equal(t, 1, m.releases)
}
