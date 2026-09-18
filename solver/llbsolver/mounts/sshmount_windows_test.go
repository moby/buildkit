//go:build windows

package mounts

import (
	"strings"
	"testing"

	"github.com/moby/buildkit/solver/pb"
	"github.com/stretchr/testify/require"
)

// TestSSHMountWindows verifies that on Windows the ssh mount is returned as a
// named-pipe mount (so the executor forwards it straight to HCS) and that
// cleanup closes the underlying pipe listener.
func TestSSHMountWindows(t *testing.T) {
	inst := &sshMountInstance{
		sm: &sshMount{
			mount: &pb.Mount{SSHOpt: &pb.SSHOpt{ID: "default"}},
		},
	}

	mounts, cleanup, err := inst.Mount()
	require.NoError(t, err)
	require.NotNil(t, cleanup)
	require.Len(t, mounts, 1)

	m := mounts[0]
	require.Equal(t, namedPipeMountType, m.Type)
	require.True(t, strings.HasPrefix(m.Source, `\\.\pipe\buildkit-ssh-`),
		"ssh mount source should be a buildkit ssh named pipe, got %q", m.Source)

	require.NoError(t, cleanup())
}
