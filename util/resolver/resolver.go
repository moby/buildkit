package resolver

import (
	"context"
	"crypto/tls"
	"net/http"
	"time"

	"github.com/containerd/containerd/remotes"
	"github.com/containerd/containerd/remotes/docker"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/auth"
	"github.com/moby/buildkit/util/tracing"
)

type RegistryConf struct {
	Mirrors   []string
	PlainHTTP *bool
	Insecure  *bool
}

func fillInsecureOpts(host string, c RegistryConf, h *docker.RegistryHost) {
	if c.PlainHTTP != nil && *c.PlainHTTP {
		h.Scheme = "http"
	} else if c.Insecure != nil && *c.Insecure {
		h.Client = &http.Client{
			Transport: tracing.NewTransport(&http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}),
		}
	} else if c.PlainHTTP == nil {
		if ok, _ := docker.MatchLocalhost(host); ok {
			h.Scheme = "http"
		}
	}
}

func NewRegistryConfig(m map[string]RegistryConf) docker.RegistryHosts {
	return docker.Registries(
		func(host string) ([]docker.RegistryHost, error) {
			c, ok := m[host]
			if !ok {
				return nil, nil
			}

			var out []docker.RegistryHost

			for _, mirror := range c.Mirrors {
				h := docker.RegistryHost{
					Scheme:       "https",
					Client:       tracing.DefaultClient,
					Host:         mirror,
					Path:         "/v2",
					Capabilities: docker.HostCapabilityPull | docker.HostCapabilityResolve,
				}
				fillInsecureOpts(mirror, m[mirror], &h)

				out = append(out, h)
			}

			if host == "docker.io" {
				host = "registry-1.docker.io"
			}

			h := docker.RegistryHost{
				Scheme:       "https",
				Client:       tracing.DefaultClient,
				Host:         host,
				Path:         "/v2",
				Capabilities: docker.HostCapabilityPush | docker.HostCapabilityPull | docker.HostCapabilityResolve,
			}
			fillInsecureOpts(host, c, &h)

			out = append(out, h)
			return out, nil
		},
		docker.ConfigureDefaultRegistries(docker.WithClient(tracing.DefaultClient), docker.WithPlainHTTP(docker.MatchLocalhost)),
	)
}

func New(ctx context.Context, hosts docker.RegistryHosts, sm *session.Manager) remotes.Resolver {
	return docker.NewResolver(docker.ResolverOptions{
		Hosts: hostsWithCredentials(ctx, hosts, sm),
	})
}

func hostsWithCredentials(ctx context.Context, hosts docker.RegistryHosts, sm *session.Manager) docker.RegistryHosts {
	id := session.FromContext(ctx)
	if id == "" {
		return hosts
	}
	return func(domain string) ([]docker.RegistryHost, error) {
		res, err := hosts(domain)
		if err != nil {
			return nil, err
		}
		if len(res) == 0 {
			return nil, nil
		}

		timeoutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		caller, err := sm.Get(timeoutCtx, id)
		if err != nil {
			return nil, err
		}

		a := docker.NewDockerAuthorizer(
			docker.WithAuthClient(res[0].Client),
			docker.WithAuthCreds(auth.CredentialsFunc(context.TODO(), caller)),
		)
		for i := range res {
			res[i].Authorizer = a
		}
		return res, nil
	}
}
