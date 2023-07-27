package llb

import (
	"testing"

	"github.com/moby/buildkit/util/gitutil"
	"github.com/stretchr/testify/assert"
)

func TestParseGitRemote(t *testing.T) {
	for _, tc := range []struct {
		remote           string
		expectedProtocol int
		expectedSSHHost  string
		expectedRemote   string
	}{
		{
			remote:           "git@github.com:moby/buildkit.git",
			expectedProtocol: gitutil.SSHProtocol,
			expectedSSHHost:  "github.com",
			expectedRemote:   "github.com/moby/buildkit.git",
		},
		{
			remote:           "ssh://github.com:moby/buildkit.git",
			expectedProtocol: gitutil.SSHProtocol,
			expectedSSHHost:  "github.com",
			expectedRemote:   "github.com/moby/buildkit.git",
		},
		{
			remote:           "ssh://github.com:buildkit.git",
			expectedProtocol: gitutil.SSHProtocol,
			expectedSSHHost:  "github.com",
			expectedRemote:   "github.com/buildkit.git",
		},
		{
			remote:           "ssh://github.com:22/moby/buildkit.git",
			expectedProtocol: gitutil.SSHProtocol,
			expectedSSHHost:  "github.com:22",
			expectedRemote:   "github.com:22/moby/buildkit.git",
		},
		{
			remote:           "ssh://github.com:22:moby/buildkit.git",
			expectedProtocol: gitutil.SSHProtocol,
			expectedSSHHost:  "github.com",
			expectedRemote:   "github.com/22:moby/buildkit.git",
		},
		{
			remote:           "git://github.com/moby/buildkit.git",
			expectedProtocol: gitutil.GitProtocol,
			expectedRemote:   "github.com/moby/buildkit.git",
		},
		{
			remote:           "http://github.com/moby/buildkit.git",
			expectedProtocol: gitutil.HTTPProtocol,
			expectedRemote:   "github.com/moby/buildkit.git",
		},
		{
			remote:           "https://github.com/moby/buildkit.git",
			expectedProtocol: gitutil.HTTPSProtocol,
			expectedRemote:   "github.com/moby/buildkit.git",
		},
	} {
		protocol, sshHost, remote := parseGitRemote(tc.remote)
		assert.Equal(t, tc.expectedProtocol, protocol, tc.remote)
		assert.Equal(t, tc.expectedSSHHost, sshHost, tc.remote)
		assert.Equal(t, tc.expectedRemote, remote, tc.remote)
	}
}
