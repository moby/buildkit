package sshutil

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsImplicitSSHTransport(t *testing.T) {
	assert.True(t, IsImplicitSSHTransport("github.com:moby/buildkit.git"))
	assert.True(t, IsImplicitSSHTransport("git@github.com:moby/buildkit.git"))
	assert.True(t, IsImplicitSSHTransport("nonstandarduser@example.com:/srv/repos/weird/project.git"))
	assert.True(t, IsImplicitSSHTransport("other_Funky-username52@example.com:path/to/repo.git/"))
	assert.True(t, IsImplicitSSHTransport("other_Funky-username52@example.com:/to/really:odd:repo.git/"))
	assert.True(t, IsImplicitSSHTransport("teddy@4houses-down.com:/~/catnip.git/"))
	assert.True(t, IsImplicitSSHTransport("weird:user@helloworld.net:foo/bar.git"))

	assert.False(t, IsImplicitSSHTransport("http://github.com/moby/buildkit"))
	assert.False(t, IsImplicitSSHTransport("github.com/moby/buildkit"))
	assert.False(t, IsImplicitSSHTransport("helloworld.net"))
	assert.False(t, IsImplicitSSHTransport("git@helloworld.net"))
	assert.False(t, IsImplicitSSHTransport("git@helloworld.net/foo/bar.git"))
	assert.False(t, IsImplicitSSHTransport(""))

	// explicit definitions are checked via isGitTransport, and are not implicit therefore this should fail:
	assert.False(t, IsImplicitSSHTransport("ssh://root@subdomain.example.hostname:2222/root/my/really/weird/path/foo.git"))
}

func TestParseSCPStyleURL(t *testing.T) {
	tests := []struct {
		url      string
		user     string
		host     string
		path     string
		fragment string
		err      bool
	}{
		{
			url: "http://github.com/moby/buildkit",
			err: true,
		},
		{
			url: "ssh://git@github.com:22/moby/buildkit.git",
			err: true,
		},
		{
			url:  "github.com:moby/buildkit.git",
			host: "github.com",
			path: "moby/buildkit.git",
		},
		{
			url:  "git@github.com:moby/buildkit.git",
			host: "github.com",
			path: "moby/buildkit.git",
			user: "git",
		},
		{
			url:      "git@github.com:moby/buildkit.git#v1.0",
			host:     "github.com",
			path:     "moby/buildkit.git",
			user:     "git",
			fragment: "v1.0",
		},
	}
	for _, test := range tests {
		t.Run(strings.ReplaceAll(test.url, "/", "_"), func(t *testing.T) {
			remote, err := ParseSCPStyleURL(test.url)
			if test.err {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, test.user, remote.User.String())
				require.Equal(t, test.host, remote.Host)
				require.Equal(t, test.path, remote.Path)
				require.Equal(t, test.fragment, remote.Fragment)
			}
		})
	}
}
