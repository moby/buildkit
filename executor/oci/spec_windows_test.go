//go:build windows

package oci

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

	realSel, err := resolveFinalPath(sel)
	require.NoError(t, err)
	realMountSource, err := resolveFinalPath(m.Source)
	require.NoError(t, err)
	require.Equal(t, realSel, realMountSource, "benign subdir must resolve to the cache subdirectory")
	require.False(t, strings.HasPrefix(m.Source, `\\?\`), "mount source should use the plain DOS path form when available")
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

// TestSubPinsSourceAgainstSwap verifies that while sub() holds the mount source,
// the entry cannot be renamed/swapped, and that releasing the cleanup lifts it.
func TestSubPinsSourceAgainstSwap(t *testing.T) {
	cacheRoot := t.TempDir()
	sel := filepath.Join(cacheRoot, "sel")
	require.NoError(t, os.MkdirAll(sel, 0700))

	_, cleanup, err := sub(mount.Mount{Source: cacheRoot}, "sel")
	require.NoError(t, err)
	require.NotNil(t, cleanup)

	require.Error(t, os.Rename(sel, sel+".swap"), "pinned source must not be renamable during the mount window")

	require.NoError(t, cleanup())
	require.NoError(t, os.Rename(sel, sel+".swap"), "source must be renamable after the pin is released")
}

// TestSubPinAllowsChildChanges verifies that the restrictive share mode applies
// to the selected directory object without making the mounted cache contents
// read-only.
func TestSubPinAllowsChildChanges(t *testing.T) {
	cacheRoot := t.TempDir()
	sel := filepath.Join(cacheRoot, "sel")
	require.NoError(t, os.MkdirAll(sel, 0700))

	_, cleanup, err := sub(mount.Mount{Source: cacheRoot}, "sel")
	require.NoError(t, err)
	require.NotNil(t, cleanup)
	defer cleanup()

	child := filepath.Join(sel, "child")
	require.NoError(t, os.WriteFile(child, []byte("first"), 0600))
	require.NoError(t, os.WriteFile(child, []byte("second"), 0600))

	renamed := filepath.Join(sel, "renamed")
	require.NoError(t, os.Rename(child, renamed))
	require.NoError(t, os.Remove(renamed))

	nested := filepath.Join(sel, "nested")
	require.NoError(t, os.Mkdir(nested, 0700))
	require.NoError(t, os.Remove(nested))
}

// TestSubUsesPinnedResolvedPath verifies that HCS receives the resolved target
// path rather than the original path containing an attacker-controlled
// junction. Replacing the original junction therefore cannot redirect the
// later mount.
func TestSubUsesPinnedResolvedPath(t *testing.T) {
	cacheRoot := t.TempDir()
	inside := filepath.Join(cacheRoot, "inside")
	require.NoError(t, os.MkdirAll(inside, 0700))

	sel := filepath.Join(cacheRoot, "sel")
	mklinkJunction(t, sel, inside)

	m, cleanup, err := sub(mount.Mount{Source: cacheRoot}, "sel")
	require.NoError(t, err)
	require.NotNil(t, cleanup)
	defer cleanup()

	realInside, err := resolveFinalPath(inside)
	require.NoError(t, err)
	realMountSource, err := resolveFinalPath(m.Source)
	require.NoError(t, err)
	require.Equal(t, realInside, realMountSource)
	require.False(t, strings.HasPrefix(m.Source, `\\?\`), "mount source should use the plain DOS path form when available")

	require.NoError(t, os.Remove(sel), "the original junction is not the pinned source object")
	outside := t.TempDir()
	mklinkJunction(t, sel, outside)

	realMountSource, err = resolveFinalPath(m.Source)
	require.NoError(t, err)
	require.Equal(t, realInside, realMountSource, "replacing the original junction must not redirect the mount source")
}

func TestMountSourcePath(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"drive letter", `\\?\C:\cache\sel`, `C:\cache\sel`},
		{"unc", `\\?\UNC\server\share\cache\sel`, `\\server\share\cache\sel`},
		{"volume guid", `\\?\Volume{11111111-1111-1111-1111-111111111111}\cache\sel`, `\\?\Volume{11111111-1111-1111-1111-111111111111}\cache\sel`},
		{"plain drive letter", `C:\cache\sel`, `C:\cache\sel`},
		{"similar prefix", `\\x\C:\cache\sel`, `\\x\C:\cache\sel`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, mountSourcePath(tc.in))
		})
	}
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
