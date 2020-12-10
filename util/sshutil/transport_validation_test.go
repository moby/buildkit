package sshutil

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsSSHTransport(t *testing.T) {
	require.False(t, IsSSHTransport("http://github.com/moby/buildkit"))
	require.False(t, IsSSHTransport("github.com/moby/buildkit"))
	require.True(t, IsSSHTransport("git@github.com:moby/buildkit.git"))
	require.True(t, IsSSHTransport("nonstandarduser@example.com:/srv/repos/weird/project.git"))
}
