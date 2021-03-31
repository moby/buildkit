package gitutil

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseProtocol(t *testing.T) {
	tests := []struct {
		url      string
		protocol int
		remote   string
	}{
		{
			url:      "http://github.com/moby/buildkit",
			protocol: HTTPProtocol,
			remote:   "github.com/moby/buildkit",
		},
		{
			url:      "https://github.com/moby/buildkit",
			protocol: HTTPSProtocol,
			remote:   "github.com/moby/buildkit",
		},
		{
			url:      "git@github.com:moby/buildkit.git",
			protocol: SSHProtocol,
			remote:   "github.com:moby/buildkit.git",
		},
		{
			url:      "nonstandarduser@example.com:/srv/repos/weird/project.git",
			protocol: SSHProtocol,
			remote:   "example.com:/srv/repos/weird/project.git",
		},
		{
			url:      "ssh://root@subdomain.example.hostname:2222/root/my/really/weird/path/foo.git",
			protocol: SSHProtocol,
			remote:   "subdomain.example.hostname:2222/root/my/really/weird/path/foo.git",
		},
		{
			url:      "git://host.xz:1234/path/to/repo.git",
			protocol: GitProtocol,
			remote:   "host.xz:1234/path/to/repo.git",
		},
	}
	for _, test := range tests {
		remote, protocol := ParseProtocol(test.url)
		require.Equal(t, remote, test.remote)
		require.Equal(t, protocol, test.protocol)
	}
}
