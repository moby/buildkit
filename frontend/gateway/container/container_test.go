package container

import (
	"testing"

	"github.com/moby/buildkit/util/system"
	"github.com/stretchr/testify/require"
)

func TestAddDefaultEnvvarWindowsCaseInsensitive(t *testing.T) {
	env := []string{"Path=C:\\custom"}

	got := addDefaultEnvvar(env, "PATH", system.DefaultPathEnvWindows, "windows")

	require.Equal(t, env, got)
}

func TestAddDefaultEnvvarLinuxCaseSensitive(t *testing.T) {
	env := []string{"Path=/custom"}

	got := addDefaultEnvvar(env, "PATH", system.DefaultPathEnvUnix, "linux")

	require.Equal(t, []string{"Path=/custom", "PATH=" + system.DefaultPathEnvUnix}, got)
}
