package nydus

import (
	"github.com/docker/distribution/reference"
	"github.com/moby/buildkit/cmd/buildkitd/config"

	"github.com/dragonflyoss/image-service/contrib/nydusify/pkg/remote"

	"github.com/containerd/containerd/remotes/docker"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/util/resolver"
	"github.com/pkg/errors"
)

// Remote communicates with remote registry
func NewRemote(
	sm *session.Manager, sid string, hosts docker.RegistryHosts, ref string, insecure bool,
) (*remote.Remote, error) {
	parsed, err := reference.ParseNormalizedNamed(ref)
	if err != nil {
		return nil, err
	}

	scope := "push"
	if insecure {
		httpTrue := true
		hosts = resolver.NewRegistryConfig(map[string]config.RegistryConfig{
			reference.Domain(parsed): {
				PlainHTTP: &httpTrue,
			},
		})
		scope += ":insecure"
	}

	resolver := resolver.DefaultPool.GetResolver(hosts, ref, scope, sm, session.NewGroup(sid))
	remote, err := remote.New(ref, resolver)
	if err != nil {
		return nil, errors.Wrap(err, "create remote instance")
	}

	return remote, nil
}
