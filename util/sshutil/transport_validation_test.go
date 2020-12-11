package sshutil

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsSSHTransport(t *testing.T) {
	require.False(t, IsSSHTransport("http://github.com/moby/buildkit"))
	require.False(t, IsSSHTransport("github.com/moby/buildkit"))
	require.False(t, IsSSHTransport("github.com:moby/buildkit.git"))
	require.False(t, IsSSHTransport("helloworld.net"))
	require.False(t, IsSSHTransport("git@helloworld.net"))
	require.False(t, IsSSHTransport("git@helloworld.net/foo/bar.git"))
	require.False(t, IsSSHTransport("bad:user@helloworld.net:foo/bar.git"))
	require.False(t, IsSSHTransport(""))
	require.True(t, IsSSHTransport("git@github.com:moby/buildkit.git"))
	require.True(t, IsSSHTransport("nonstandarduser@example.com:/srv/repos/weird/project.git"))
	require.True(t, IsSSHTransport("other_Funky-username52@example.com:path/to/repo.git/"))
	require.True(t, IsSSHTransport("other_Funky-username52@example.com:/to/really:odd:repo.git/"))
	require.True(t, IsSSHTransport("teddy@4houses-down.com:/~/catnip.git/"))
}
