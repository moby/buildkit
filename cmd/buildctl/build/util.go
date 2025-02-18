package build

import (
	"os"
	"strconv"

	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/util/bklog"
	"github.com/pkg/errors"
)

// loadGithubEnv verify that url and token attributes exists in the
// cache.
// If not, it will search for $ACTIONS_RUNTIME_TOKEN and $ACTIONS_CACHE_URL
// environments variables and add it to cache Options
// Since it works for both import and export
func loadGithubEnv(cache client.CacheOptionsEntry) (client.CacheOptionsEntry, error) {
	version, ok := cache.Attrs["version"]
	if !ok {
		// https://github.com/actions/toolkit/blob/2b08dc18f261b9fdd978b70279b85cbef81af8bc/packages/cache/src/internal/config.ts#L19
		if v, ok := os.LookupEnv("ACTIONS_CACHE_SERVICE_V2"); ok {
			if b, err := strconv.ParseBool(v); err == nil && b {
				version = "2"
			}
		}
	}

	if _, ok := cache.Attrs["url_v2"]; !ok && version == "2" {
		// https://github.com/actions/toolkit/blob/2b08dc18f261b9fdd978b70279b85cbef81af8bc/packages/cache/src/internal/config.ts#L34-L35
		if v, ok := os.LookupEnv("ACTIONS_RESULTS_URL"); ok {
			cache.Attrs["url_v2"] = v
		}
	}
	if _, ok := cache.Attrs["url"]; !ok {
		// https://github.com/actions/toolkit/blob/2b08dc18f261b9fdd978b70279b85cbef81af8bc/packages/cache/src/internal/config.ts#L28-L33
		if v, ok := os.LookupEnv("ACTIONS_CACHE_URL"); ok {
			cache.Attrs["url"] = v
		} else if v, ok := os.LookupEnv("ACTIONS_RESULTS_URL"); ok {
			cache.Attrs["url"] = v
		}
	}
	if _, ok := cache.Attrs["url"]; !ok {
		if _, ok := cache.Attrs["url_v2"]; !ok {
			return cache, errors.New("cache with type gha requires url parameter to be set")
		}
	}

	if _, ok := cache.Attrs["token"]; !ok {
		token, ok := os.LookupEnv("ACTIONS_RUNTIME_TOKEN")
		if !ok {
			return cache, errors.New("cache with type gha requires token parameter or $ACTIONS_RUNTIME_TOKEN")
		}
		cache.Attrs["token"] = token
	}
	return cache, nil
}

// loadOptEnv loads opt values from the environment.
// The returned map is always non-nil.
func loadOptEnv() map[string]string {
	m := make(map[string]string)
	propagatableEnvs := []string{"SOURCE_DATE_EPOCH"}
	for _, env := range propagatableEnvs {
		if v, ok := os.LookupEnv(env); ok {
			bklog.L.Debugf("Propagating %s from the client env to the build arg", env)
			m["build-arg:"+env] = v
		}
	}
	return m
}
