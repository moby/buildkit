package local

import (
	"io/fs"
	"sort"
	"testing"

	"github.com/containerd/continuity/fs/fstest"
	"github.com/stretchr/testify/require"
	"github.com/tonistiigi/fsutil"
)

func TestCreateFSOptsLoadSource(t *testing.T) {
	tests := []struct {
		name    string
		attrs   map[string]string
		want    string
		wantErr string
	}{
		{
			name:  "unset",
			attrs: map[string]string{},
			want:  "",
		},
		{
			name:    "empty",
			attrs:   map[string]string{keySource: ""},
			wantErr: "empty value for src",
		},
		{
			name:    "whitespace only",
			attrs:   map[string]string{keySource: "   "},
			wantErr: "empty value for src",
		},
		{
			name:  "absolute",
			attrs: map[string]string{keySource: "/app/build"},
			want:  "/app/build",
		},
		{
			name:  "trailing slash",
			attrs: map[string]string{keySource: "/app/build/"},
			want:  "/app/build",
		},
		{
			name:  "relative is anchored to root",
			attrs: map[string]string{keySource: "app/build"},
			want:  "/app/build",
		},
		{
			name:  "dot slash prefix",
			attrs: map[string]string{keySource: "./app/build"},
			want:  "/app/build",
		},
		{
			name:  "redundant separators",
			attrs: map[string]string{keySource: "/a//b/./c"},
			want:  "/a/b/c",
		},
		{
			name:  "dot is root",
			attrs: map[string]string{keySource: "."},
			want:  "/",
		},
		{
			name:  "slash is root",
			attrs: map[string]string{keySource: "/"},
			want:  "/",
		},
		{
			name:  "surrounding whitespace is trimmed",
			attrs: map[string]string{keySource: "  /app  "},
			want:  "/app",
		},
		{
			// Clamping at the root matches what BuildKit already does for
			// container-side paths: COPY --from, and both source and target of
			// RUN --mount, accept ".." and clamp it the same way.
			name:  "parent traversal is clamped to root",
			attrs: map[string]string{keySource: "../../etc"},
			want:  "/etc",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var opts CreateFSOpts
			rest, err := opts.Load(tc.attrs)

			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				return
			}

			require.NoError(t, err)
			require.Equal(t, tc.want, opts.Source)
			require.NotContains(t, rest, keySource)
		})
	}
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
		names = append(names, p)
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
	}{
		{
			name:   "empty source exports the whole mount",
			source: "",
			want:   []string{"app", "etc", "etc/inside.txt", "rel", "sub", "sub/nested.txt", "top.txt"},
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
			// same clamping as COPY --from and RUN --mount, see the Load test
			name:   "parent traversal is clamped to the mount",
			source: "../../etc",
			want:   []string{"inside.txt"},
		},
		{
			name:    "missing path fails",
			source:  "/nope",
			wantErr: "src=/nope no such file or directory",
		},
		{
			name:    "file is not a directory",
			source:  "/top.txt",
			wantErr: "src=/top.txt not a directory",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			outputFS, err := resolveSafeSource(newRootfs(t), tc.source)
			if tc.wantErr != "" {
				require.EqualError(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, walkNames(t, outputFS))
		})
	}
}
