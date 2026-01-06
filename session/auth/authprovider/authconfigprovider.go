package authprovider

import (
	"context"
	"sync"
	"time"

	"github.com/docker/cli/cli/config/configfile"
	"github.com/docker/cli/cli/config/types"
)

func LoadAuthConfig(config *configfile.ConfigFile) AuthConfigProvider {
	acp := &authConfigProvider{
		config:          config,
		authConfigCache: map[string]authConfigCacheEntry{},
	}
	return acp.load
}

type authConfigProvider struct {
	config          *configfile.ConfigFile
	authConfigCache map[string]authConfigCacheEntry
	mu              sync.Mutex
}

func (ap *authConfigProvider) load(ctx context.Context, host string, scopes []string, cacheExpireCheck ExpireCachedAuthCheck) (types.AuthConfig, error) {
	ap.mu.Lock()
	defer ap.mu.Unlock()

	entry, exists := ap.authConfigCache[host]
	if exists && (cacheExpireCheck == nil || !cacheExpireCheck(entry.Created, host)) {
		return *entry.Auth, nil
	}

	hostKey := host
	if host == DockerHubRegistryHost {
		hostKey = DockerHubConfigfileKey
	}

	ac, err := ap.config.GetAuthConfig(hostKey)
	if err != nil {
		return types.AuthConfig{}, err
	}

	entry = authConfigCacheEntry{
		Created: time.Now(),
		Auth:    &ac,
	}

	ap.authConfigCache[host] = entry

	return ac, nil
}

type authConfigCacheEntry struct {
	Created time.Time
	Auth    *types.AuthConfig
}
