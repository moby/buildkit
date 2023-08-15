package gitutil

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseURL(t *testing.T) {
	tests := []struct {
		url      string
		protocol string
		host     string
		fragment string
		path     string
		user     string
		err      bool
	}{
		{
			url:      "http://github.com/moby/buildkit",
			protocol: HTTPProtocol,
			host:     "github.com",
			path:     "/moby/buildkit",
		},
		{
			url:      "https://github.com/moby/buildkit",
			protocol: HTTPSProtocol,
			host:     "github.com",
			path:     "/moby/buildkit",
		},
		{
			url:      "http://github.com/moby/buildkit#v1.0.0",
			protocol: HTTPProtocol,
			host:     "github.com",
			path:     "/moby/buildkit",
			fragment: "v1.0.0",
		},
		{
			url:      "http://foo:bar@github.com/moby/buildkit#v1.0.0",
			protocol: HTTPProtocol,
			host:     "github.com",
			path:     "/moby/buildkit",
			fragment: "v1.0.0",
			user:     "foo:bar",
		},
		{
			url:      "ssh://git@github.com/moby/buildkit.git",
			protocol: SSHProtocol,
			host:     "github.com",
			path:     "/moby/buildkit.git",
			user:     "git",
		},
		{
			url:      "ssh://git@github.com:22/moby/buildkit.git",
			protocol: SSHProtocol,
			host:     "github.com:22",
			path:     "/moby/buildkit.git",
			user:     "git",
		},
		{
			url:      "git@github.com:moby/buildkit.git",
			protocol: SSHProtocol,
			host:     "github.com",
			path:     "/moby/buildkit.git",
			user:     "git",
		},
		{
			url:      "git@github.com:moby/buildkit.git#v1.0.0",
			protocol: SSHProtocol,
			host:     "github.com",
			path:     "/moby/buildkit.git",
			fragment: "v1.0.0",
			user:     "git",
		},
		{
			url:      "nonstandarduser@example.com:/srv/repos/weird/project.git",
			protocol: SSHProtocol,
			host:     "example.com",
			path:     "/srv/repos/weird/project.git",
			user:     "nonstandarduser",
		},
		{
			url:      "ssh://root@subdomain.example.hostname:2222/root/my/really/weird/path/foo.git",
			protocol: SSHProtocol,
			host:     "subdomain.example.hostname:2222",
			path:     "/root/my/really/weird/path/foo.git",
			user:     "root",
		},
		{
			url:      "git://host.xz:1234/path/to/repo.git",
			protocol: GitProtocol,
			host:     "host.xz:1234",
			path:     "/path/to/repo.git",
		},
		{
			url: "httpx://github.com/moby/buildkit",
			err: true,
		},
		{
			url:      "HTTP://github.com/moby/buildkit",
			protocol: HTTPProtocol,
			host:     "github.com",
			path:     "/moby/buildkit",
		},
	}
	for _, test := range tests {
		t.Run(test.url, func(t *testing.T) {
			remote, err := ParseURL(test.url)
			if test.err {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, test.protocol, remote.Scheme)
				require.Equal(t, test.host, remote.Host)
				require.Equal(t, test.path, remote.Path)
				require.Equal(t, test.fragment, remote.Fragment)
				require.Equal(t, test.user, remote.User.String())
			}
		})
	}
}
