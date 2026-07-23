//go:build windows

package mounts

import (
	"os"
	"strings"
	"testing"

	"github.com/moby/buildkit/solver/pb"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

// TestSecretMountWindows verifies the secret is returned as a single-file,
// read-only bind mount with a restrictive DACL, and cleanup removes it.
func TestSecretMountWindows(t *testing.T) {
	data := []byte("super-secret-value")
	inst := &secretMountInstance{
		sm: &secretMount{
			mount: &pb.Mount{SecretOpt: &pb.SecretOpt{ID: "mysecret"}},
			data:  data,
		},
	}

	mounts, cleanup, err := inst.Mount()
	require.NoError(t, err)
	require.NotNil(t, cleanup)
	require.Len(t, mounts, 1)

	m := mounts[0]
	require.Equal(t, "bind", m.Type)
	require.Contains(t, m.Options, "ro")
	require.NotContains(t, m.Options, "tmpfs")

	got, err := os.ReadFile(m.Source)
	require.NoError(t, err)
	require.Equal(t, data, got)

	// DACL must be protected (D:P) and grant SYSTEM but no broad principals
	// (Everyone WD/S-1-1-0, Users BU/S-1-5-32-545).
	sd, err := windows.GetNamedSecurityInfo(m.Source, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	require.NoError(t, err)
	sddl := sd.String()
	require.Contains(t, sddl, "(A;;FA;;;SY)")
	require.NotContains(t, sddl, ";WD)")
	require.NotContains(t, sddl, "S-1-1-0")
	require.NotContains(t, sddl, ";BU)")
	require.NotContains(t, sddl, "S-1-5-32-545")
	require.True(t, strings.HasPrefix(sddl, "D:P"), "DACL should be protected, got %q", sddl)

	// The parent temp dir must also carry a protected DACL so the secret file
	// is born restricted rather than relying on inherited %TEMP% permissions.
	dsd, err := windows.GetNamedSecurityInfo(inst.root, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	require.NoError(t, err)
	dsddl := dsd.String()
	require.True(t, strings.HasPrefix(dsddl, "D:P"), "dir DACL should be protected, got %q", dsddl)
	require.NotContains(t, dsddl, "S-1-1-0")
	require.NotContains(t, dsddl, "S-1-5-32-545")

	require.NoError(t, cleanup())

	_, err = os.Stat(m.Source)
	require.True(t, os.IsNotExist(err), "secret file should be removed after cleanup")
}

// TestRestrictPathACLInheritable verifies a directory ACL is written with
// inheritance flags so files created inside inherit the restriction.
func TestRestrictPathACLInheritable(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, restrictPathACL(dir, true))
	sd, err := windows.GetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	require.NoError(t, err)
	// OICI => "OICI" flag field renders in the SDDL ACE strings.
	require.Contains(t, sd.String(), "OICI", "dir ACEs should be object/container inheritable")
}
