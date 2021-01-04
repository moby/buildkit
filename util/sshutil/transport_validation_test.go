package sshutil

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsImplicitSSHTransport(t *testing.T) {
	require.False(t, IsImplicitSSHTransport("http://github.com/moby/buildkit"))
	require.False(t, IsImplicitSSHTransport("github.com/moby/buildkit"))
	require.False(t, IsImplicitSSHTransport("github.com:moby/buildkit.git"))
	require.False(t, IsImplicitSSHTransport("helloworld.net"))
	require.False(t, IsImplicitSSHTransport("git@helloworld.net"))
	require.False(t, IsImplicitSSHTransport("git@helloworld.net/foo/bar.git"))
	require.False(t, IsImplicitSSHTransport("bad:user@helloworld.net:foo/bar.git"))
	require.False(t, IsImplicitSSHTransport(""))
	require.True(t, IsImplicitSSHTransport("git@github.com:moby/buildkit.git"))
	require.True(t, IsImplicitSSHTransport("nonstandarduser@example.com:/srv/repos/weird/project.git"))
	require.True(t, IsImplicitSSHTransport("other_Funky-username52@example.com:path/to/repo.git/"))
	require.True(t, IsImplicitSSHTransport("other_Funky-username52@example.com:/to/really:odd:repo.git/"))
	require.True(t, IsImplicitSSHTransport("teddy@4houses-down.com:/~/catnip.git/"))

	// explicit definitions are checked via isGitTransport, and are not implicit therefore this should fail:
	require.False(t, IsImplicitSSHTransport("ssh://root@subdomain.example.hostname:2222/root/my/really/weird/path/foo.git"))
}
