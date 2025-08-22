package gitutil

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseURL(t *testing.T) {
	tests := []struct {
		url    string
		result GitURL
		err    bool
	}{
		{
			url: "http://github.com/moby/buildkit",
			result: GitURL{
				Scheme: HTTPProtocol,
				Host:   "github.com",
				Path:   "/moby/buildkit",
			},
		},
		{
			url: "https://github.com/moby/buildkit",
			result: GitURL{
				Scheme: HTTPSProtocol,
				Host:   "github.com",
				Path:   "/moby/buildkit",
			},
		},
		{
			url: "http://github.com/moby/buildkit#v1.0.0",
			result: GitURL{
				Scheme: HTTPProtocol,
				Host:   "github.com",
				Path:   "/moby/buildkit",
				Opts:   &GitURLOpts{Ref: "v1.0.0"},
			},
		},
		{
			url: "http://github.com/moby/buildkit#v1.0.0:subdir",
			result: GitURL{
				Scheme: HTTPProtocol,
				Host:   "github.com",
				Path:   "/moby/buildkit",
				Opts:   &GitURLOpts{Ref: "v1.0.0", Subdir: "subdir"},
			},
		},
		{
			url: "http://foo:bar@github.com/moby/buildkit#v1.0.0",
			result: GitURL{
				Scheme: HTTPProtocol,
				Host:   "github.com",
				Path:   "/moby/buildkit",
				Opts:   &GitURLOpts{Ref: "v1.0.0"},
				User:   url.UserPassword("foo", "bar"),
			},
		},
		{
			url: "ssh://git@github.com/moby/buildkit.git",
			result: GitURL{
				Scheme: SSHProtocol,
				Host:   "github.com",
				Path:   "/moby/buildkit.git",
				User:   url.User("git"),
			},
		},
		{
			url: "ssh://git@github.com:22/moby/buildkit.git",
			result: GitURL{
				Scheme: SSHProtocol,
				Host:   "github.com:22",
				Path:   "/moby/buildkit.git",
				User:   url.User("git"),
			},
		},
		{
			url: "git@github.com:moby/buildkit.git",
			result: GitURL{
				Scheme: SSHProtocol,
				Host:   "github.com",
				Path:   "moby/buildkit.git",
				User:   url.User("git"),
			},
		},
		{
			url: "git@github.com:moby/buildkit.git#v1.0.0",
			result: GitURL{
				Scheme: SSHProtocol,
				Host:   "github.com",
				Path:   "moby/buildkit.git",
				Opts:   &GitURLOpts{Ref: "v1.0.0"},
				User:   url.User("git"),
			},
		},
		{
			url: "git@github.com:moby/buildkit.git#v1.0.0:hack",
			result: GitURL{
				Scheme: SSHProtocol,
				Host:   "github.com",
				Path:   "moby/buildkit.git",
				Opts:   &GitURLOpts{Ref: "v1.0.0", Subdir: "hack"},
				User:   url.User("git"),
			},
		},
		{
			url: "nonstandarduser@example.com:/srv/repos/weird/project.git",
			result: GitURL{
				Scheme: SSHProtocol,
				Host:   "example.com",
				Path:   "/srv/repos/weird/project.git",
				User:   url.User("nonstandarduser"),
			},
		},
		{
			url: "ssh://root@subdomain.example.hostname:2222/root/my/really/weird/path/foo.git",
			result: GitURL{
				Scheme: SSHProtocol,
				Host:   "subdomain.example.hostname:2222",
				Path:   "/root/my/really/weird/path/foo.git",
				User:   url.User("root"),
			},
		},
		{
			url: "git://host.xz:1234/path/to/repo.git",
			result: GitURL{
				Scheme: GitProtocol,
				Host:   "host.xz:1234",
				Path:   "/path/to/repo.git",
			},
		},
		{
			url: "ssh://someuser@192.168.0.123:456/~/repo-in-my-home-dir.git",
			result: GitURL{
				Scheme: SSHProtocol,
				Host:   "192.168.0.123:456",
				Path:   "/~/repo-in-my-home-dir.git",
				User:   url.User("someuser"),
			},
		},
		{
			url: "httpx://github.com/moby/buildkit",
			err: true,
		},
		{
			url: "HTTP://github.com/moby/buildkit",
			result: GitURL{
				Scheme: HTTPProtocol,
				Host:   "github.com",
				Path:   "/moby/buildkit",
			},
		},
		{
			url: "https://github.com/moby/buildkit?ref=v1.0.0&subdir=/subdir",
			result: GitURL{
				Scheme: HTTPSProtocol,
				Host:   "github.com",
				Path:   "/moby/buildkit",
				Opts:   &GitURLOpts{Ref: "v1.0.0", Subdir: "/subdir"},
			},
		},
		{
			url: "https://github.com/moby/buildkit?subdir=/subdir#v1.0.0",
			result: GitURL{
				Scheme: HTTPSProtocol,
				Host:   "github.com",
				Path:   "/moby/buildkit",
				Opts:   &GitURLOpts{Ref: "v1.0.0", Subdir: "/subdir"},
			},
		},
		{
			url: "https://github.com/moby/buildkit?tag=v1.0.0",
			result: GitURL{
				Scheme: HTTPSProtocol,
				Host:   "github.com",
				Path:   "/moby/buildkit",
				Opts:   &GitURLOpts{Ref: "refs/tags/v1.0.0"},
			},
		},
		{
			url: "https://github.com/moby/buildkit?branch=v1.0",
			result: GitURL{
				Scheme: HTTPSProtocol,
				Host:   "github.com",
				Path:   "/moby/buildkit",
				Opts:   &GitURLOpts{Ref: "refs/heads/v1.0"},
			},
		},
		{
			url: "https://github.com/moby/buildkit?ref=v1.0.0#v1.2.3",
			err: true,
		},
		{
			url: "https://github.com/moby/buildkit?ref=v1.0.0&tag=v1.2.3",
			err: true,
		},
		{
			// TODO: consider allowing this, when the tag actually exists on the branch
			url: "https://github.com/moby/buildkit?tag=v1.0.0&branch=v1.0",
			err: true,
		},
		{
			url: "git@github.com:moby/buildkit.git?subdir=/subdir#v1.0.0",
			result: GitURL{
				Scheme: SSHProtocol,
				Host:   "github.com",
				Path:   "moby/buildkit.git",
				User:   url.User("git"),
				Opts:   &GitURLOpts{Ref: "v1.0.0", Subdir: "/subdir"},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.url, func(t *testing.T) {
			remote, err := ParseURL(test.url)
			if test.err {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, test.result.Scheme, remote.Scheme)
				require.Equal(t, test.result.Host, remote.Host)
				require.Equal(t, test.result.Path, remote.Path)
				require.Equal(t, test.result.Opts, remote.Opts)
				require.Equal(t, test.result.User.String(), remote.User.String())
			}
		})
	}
}
