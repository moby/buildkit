//go:build windows

package sshforward

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

func TestAgentPipeSecurityDescriptor(t *testing.T) {
	sd, err := sshPipeSecurityDescriptor()
	require.NoError(t, err)

	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	require.NoError(t, err)
	require.NotContains(t, sd, ";;;AU)")
	require.Contains(t, sd, ";;;"+user.User.Sid.String()+")")
	require.Contains(t, sd, ";;;"+windowsContainerGroupSID+")")
}

func TestMountSSHSocketUsesContainerDescriptor(t *testing.T) {
	path, cleanup, err := MountSSHSocket(t.Context(), nil, SocketOpt{ID: "default"})
	require.NoError(t, err)
	require.NotNil(t, cleanup)
	require.True(t, strings.HasPrefix(path, `\\.\pipe\buildkit-ssh-`),
		"expected a buildkit ssh named pipe, got %q", path)

	require.NoError(t, cleanup())
}
