//go:build windows

package sshforward

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSetAgentPipeSecurityDescriptor verifies the override hook: an empty
// descriptor is ignored (keeping the container-reachable default) while a
// non-empty descriptor replaces it.
func TestSetAgentPipeSecurityDescriptor(t *testing.T) {
	orig := sshPipeSecurityDescriptor
	t.Cleanup(func() { sshPipeSecurityDescriptor = orig })

	require.Equal(t, defaultSSHPipeSecurityDescriptor, sshPipeSecurityDescriptor)

	// Empty is ignored.
	SetAgentPipeSecurityDescriptor("")
	require.Equal(t, defaultSSHPipeSecurityDescriptor, sshPipeSecurityDescriptor)

	// Non-empty overrides.
	custom := "D:P(A;;GA;;;BA)(A;;GA;;;SY)"
	SetAgentPipeSecurityDescriptor(custom)
	require.Equal(t, custom, sshPipeSecurityDescriptor)
}

// TestMountSSHSocketUsesConfiguredDescriptor verifies MountSSHSocket creates a
// listenable buildkit ssh pipe regardless of the configured security
// descriptor, and that cleanup closes it.
func TestMountSSHSocketUsesConfiguredDescriptor(t *testing.T) {
	orig := sshPipeSecurityDescriptor
	t.Cleanup(func() { sshPipeSecurityDescriptor = orig })

	// A restrictive but valid descriptor (Administrators + SYSTEM) must still
	// allow the daemon to create the pipe.
	SetAgentPipeSecurityDescriptor("D:P(A;;GA;;;BA)(A;;GA;;;SY)")

	path, cleanup, err := MountSSHSocket(t.Context(), nil, SocketOpt{ID: "default"})
	require.NoError(t, err)
	require.NotNil(t, cleanup)
	require.True(t, strings.HasPrefix(path, `\\.\pipe\buildkit-ssh-`),
		"expected a buildkit ssh named pipe, got %q", path)

	require.NoError(t, cleanup())
}
