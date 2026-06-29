//go:build windows

package oci

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/containerd/containerd/v2/core/mount"
	"github.com/stretchr/testify/require"
)

// mklinkJunction creates a Windows directory junction (mklink /J).
func mklinkJunction(t *testing.T, link, target string) {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "cmd", "/c", "mklink", "/J", link, target).CombinedOutput()
	require.NoErrorf(t, err, "mklink /J %q %q failed: %s", link, target, out)
}

// mklinkSymlinkDir creates a Windows directory symlink (mklink /D). It may need
// Developer Mode or SeCreateSymbolicLinkPrivilege; callers should skip on error.
func mklinkSymlinkDir(t *testing.T, link, target string) error {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "cmd", "/c", "mklink", "/D", link, target).CombinedOutput()
	if err != nil {
		t.Logf("mklink /D %q %q failed (symlink creation may be unprivileged): %s", link, target, out)
	}
	return err
}

// TestSubResolvesBenignSubdir checks that a normal cache subdirectory resolves
// to a path inside the cache root.
func TestSubResolvesBenignSubdir(t *testing.T) {
	cacheRoot := t.TempDir()
	sel := filepath.Join(cacheRoot, "sel")
	require.NoError(t, os.MkdirAll(sel, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(sel, "benign.txt"), []byte("benign"), 0600))

	m, cleanup, err := sub(mount.Mount{Source: cacheRoot}, "sel")
	require.NoError(t, err)
	if cleanup != nil {
		defer cleanup()
	}

	require.Equal(t, sel, m.Source, "benign subdir must resolve to the cache subdirectory")
}

// TestSubRejectsJunctionEscape is the core regression test: a cache subdir
// replaced by a junction pointing outside the cache root must be rejected.
func TestSubRejectsJunctionEscape(t *testing.T) {
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "marker.txt"), []byte("OUTSIDE"), 0600))

	cacheRoot := t.TempDir()
	sel := filepath.Join(cacheRoot, "sel")
	mklinkJunction(t, sel, outside)

	m, cleanup, err := sub(mount.Mount{Source: cacheRoot}, "sel")
	if cleanup != nil {
		defer cleanup()
	}
	require.Error(t, err, "sub() must reject a junction that escapes the cache root")
	require.NotEqual(t, outside, m.Source, "resolved source must not be the outside host path")
}

// TestSubRejectsSymlinkEscape verifies the same protection for a directory
// symbolic link (mklink /D) pointing outside the cache root.
func TestSubRejectsSymlinkEscape(t *testing.T) {
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "marker.txt"), []byte("OUTSIDE"), 0600))

	cacheRoot := t.TempDir()
	sel := filepath.Join(cacheRoot, "sel")
	if err := mklinkSymlinkDir(t, sel, outside); err != nil {
		t.Skipf("could not create directory symlink (insufficient privilege?): %v", err)
	}

	m, cleanup, err := sub(mount.Mount{Source: cacheRoot}, "sel")
	if cleanup != nil {
		defer cleanup()
	}
	require.Error(t, err, "sub() must reject a symlink that escapes the cache root")
	require.NotEqual(t, outside, m.Source, "resolved source must not be the outside host path")
}

// TestSubRejectsNestedJunctionEscape checks a junction in a non-terminal path
// component (source "mid/leaf" where "mid" escapes the root) is also rejected.
func TestSubRejectsNestedJunctionEscape(t *testing.T) {
	outside := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(outside, "leaf"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(outside, "leaf", "marker.txt"), []byte("OUTSIDE"), 0600))

	cacheRoot := t.TempDir()
	mid := filepath.Join(cacheRoot, "mid")
	mklinkJunction(t, mid, outside)

	m, cleanup, err := sub(mount.Mount{Source: cacheRoot}, "mid/leaf")
	if cleanup != nil {
		defer cleanup()
	}
	require.Error(t, err, "sub() must reject a path that traverses a junction outside the cache root")
	require.NotEqual(t, filepath.Join(outside, "leaf"), m.Source, "resolved source must not escape the cache root")
}

func TestPathWithinRoot(t *testing.T) {
	cases := []struct {
		name string
		root string
		p    string
		want bool
	}{
		{"equal", `C:\cache`, `C:\cache`, true},
		{"equal trailing sep on p", `C:\cache`, `C:\cache\`, true},
		{"equal trailing sep on root", `C:\cache\`, `C:\cache`, true},
		{"direct child", `C:\cache`, `C:\cache\sel`, true},
		{"nested child", `C:\cache`, `C:\cache\a\b`, true},
		{"case-insensitive child", `C:\Cache`, `c:\cache\sel`, true},
		{"child name starting with dotdot", `C:\cache`, `C:\cache\..foo`, true},
		{"exact parent", `C:\cache`, `C:\`, false},
		{"sibling prefix confusion", `C:\cache`, `C:\cache-evil`, false},
		{"unrelated escape", `C:\cache`, `C:\Windows`, false},
		{"different volume", `C:\cache`, `D:\cache\sel`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, pathWithinRoot(tc.root, tc.p))
		})
	}
}
