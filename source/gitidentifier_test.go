package source

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewGitIdentifier(t *testing.T) {
	gi, err := NewGitIdentifier("ssh://root@subdomain.example.hostname:2222/root/my/really/weird/path/foo.git")
	require.Nil(t, err)
	require.Equal(t, "ssh://root@subdomain.example.hostname:2222/root/my/really/weird/path/foo.git", gi.Remote)
	require.Equal(t, "", gi.Ref)
	require.Equal(t, "", gi.Subdir)

	gi, err = NewGitIdentifier("ssh://root@subdomain.example.hostname:2222/root/my/really/weird/path/foo.git#main")
	require.Nil(t, err)
	require.Equal(t, "ssh://root@subdomain.example.hostname:2222/root/my/really/weird/path/foo.git", gi.Remote)
	require.Equal(t, "main", gi.Ref)
	require.Equal(t, "", gi.Subdir)

	gi, err = NewGitIdentifier("git@github.com:moby/buildkit.git")
	require.Nil(t, err)
	require.Equal(t, "git@github.com:moby/buildkit.git", gi.Remote)
	require.Equal(t, "", gi.Ref)
	require.Equal(t, "", gi.Subdir)

	gi, err = NewGitIdentifier("github.com/moby/buildkit.git#main")
	require.Nil(t, err)
	require.Equal(t, "https://github.com/moby/buildkit.git", gi.Remote)
	require.Equal(t, "main", gi.Ref)
	require.Equal(t, "", gi.Subdir)
}
