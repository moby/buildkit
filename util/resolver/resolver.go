package resolver

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/containerd/containerd/remotes"
	"github.com/containerd/containerd/remotes/docker"
	"github.com/moby/buildkit/cmd/buildkitd/config"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/auth"
	"github.com/moby/buildkit/util/tracing"
	"github.com/pkg/errors"
)

func fillInsecureOpts(host string, c config.RegistryConfig, h *docker.RegistryHost) error {
	tc, err := loadTLSConfig(c)
	if err != nil {
		return err
	}

	if c.PlainHTTP != nil && *c.PlainHTTP {
		h.Scheme = "http"
	} else if c.Insecure != nil && *c.Insecure {
		if tc == nil {
			tc = &tls.Config{}
		}
		tc.InsecureSkipVerify = true
	} else if c.PlainHTTP == nil {
		if ok, _ := docker.MatchLocalhost(host); ok {
			h.Scheme = "http"
		}
	}

	if tc != nil && h.Scheme != "http" {
		h.Client = &http.Client{
			Transport: tracing.NewTransport(&http.Transport{TLSClientConfig: tc}),
		}
	}

	return nil
}

func loadTLSConfig(c config.RegistryConfig) (*tls.Config, error) {
	for _, d := range c.TLSConfigDir {
		fs, err := ioutil.ReadDir(d)
		if err != nil && !os.IsNotExist(err) && !os.IsPermission(err) {
			return nil, errors.WithStack(err)
		}
		for _, f := range fs {
			if strings.HasSuffix(f.Name(), ".crt") {
				c.RootCAs = append(c.RootCAs, filepath.Join(d, f.Name()))
			}
			if strings.HasSuffix(f.Name(), ".cert") {
				c.KeyPairs = append(c.KeyPairs, config.TLSKeyPair{
					Certificate: filepath.Join(d, f.Name()),
					Key:         filepath.Join(d, strings.TrimSuffix(f.Name(), ".cert")+".key"),
				})
			}
		}
	}

	var tc *tls.Config

	if len(c.RootCAs) > 0 {
		tc = &tls.Config{}
		systemPool, err := x509.SystemCertPool()
		if err != nil {
			if runtime.GOOS == "windows" {
				systemPool = x509.NewCertPool()
			} else {
				return nil, errors.Wrapf(err, "unable to get system cert pool")
			}
		}
		tc.RootCAs = systemPool
	}

	for _, p := range c.RootCAs {
		dt, err := ioutil.ReadFile(p)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to read %s", p)
		}
		tc.RootCAs.AppendCertsFromPEM(dt)
	}

	for _, kp := range c.KeyPairs {
		cert, err := tls.LoadX509KeyPair(kp.Certificate, kp.Key)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to load keypair for %s", kp.Certificate)
		}
		if tc == nil {
			tc = &tls.Config{}
		}
		tc.Certificates = append(tc.Certificates, cert)
	}

	return tc, nil
}

func NewRegistryConfig(m map[string]config.RegistryConfig) docker.RegistryHosts {
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

				if err := fillInsecureOpts(mirror, m[mirror], &h); err != nil {
					return nil, err
				}

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

			if err := fillInsecureOpts(host, c, &h); err != nil {
				return nil, err
			}

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
