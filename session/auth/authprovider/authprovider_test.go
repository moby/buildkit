package authprovider

import (
	"context"
	"testing"
	"time"

	"github.com/docker/cli/cli/config/configfile"
	"github.com/docker/cli/cli/config/types"
	"github.com/moby/buildkit/session/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFetchTokenCaching(t *testing.T) {
	newCfg := func() *configfile.ConfigFile {
		return &configfile.ConfigFile{
			AuthConfigs: map[string]types.AuthConfig{
				dockerHubConfigfileKey: {Username: "user", RegistryToken: "hunter2"},
			},
		}
	}

	cfg := newCfg()
	p := NewDockerAuthProvider(DockerAuthProviderConfig{
		ConfigFile: cfg,
	}).(*authProvider)
	res, err := p.FetchToken(context.Background(), &auth.FetchTokenRequest{Host: dockerHubRegistryHost})
	require.NoError(t, err)
	assert.Equal(t, "hunter2", res.Token)

	cfg.AuthConfigs[dockerHubConfigfileKey] = types.AuthConfig{Username: "user", RegistryToken: "hunter3"}
	res, err = p.FetchToken(context.Background(), &auth.FetchTokenRequest{Host: dockerHubRegistryHost})
	require.NoError(t, err)

	// Verify that we cached the result instead of returning hunter3.
	assert.Equal(t, "hunter2", res.Token)

	// Now again but this time expire the auth.

	cfg = newCfg()
	p = NewDockerAuthProvider(DockerAuthProviderConfig{
		ConfigFile: cfg,
		ExpireCachedAuth: func(_ time.Time, host string) bool {
			require.Equal(t, dockerHubConfigfileKey, host)
			return true
		},
	}).(*authProvider)

	res, err = p.FetchToken(context.Background(), &auth.FetchTokenRequest{Host: dockerHubRegistryHost})
	require.NoError(t, err)
	assert.Equal(t, "hunter2", res.Token)

	cfg.AuthConfigs[dockerHubConfigfileKey] = types.AuthConfig{Username: "user", RegistryToken: "hunter3"}
	res, err = p.FetchToken(context.Background(), &auth.FetchTokenRequest{Host: dockerHubRegistryHost})
	require.NoError(t, err)

	// Verify that we re-fetched the token after it expired.
	assert.Equal(t, "hunter3", res.Token)
}
