package resolver

import (
	"crypto/tls"
	"crypto/x509"
	"io/ioutil"
	"math/rand"
	"net/http"

	"github.com/containerd/containerd/remotes/docker"
	"github.com/docker/distribution/reference"
	"github.com/moby/buildkit/util/tracing"
	"github.com/opentracing-contrib/go-stdlib/nethttp"
	"github.com/sirupsen/logrus"
)

type RegistryConf struct {
	Mirrors   []string
	PlainHTTP bool
	ExtraCA   string
}

type ResolveOptionsFunc func(string) docker.ResolverOptions

func NewResolveOptionsFunc(m map[string]RegistryConf) ResolveOptionsFunc {
	return func(ref string) docker.ResolverOptions {
		def := docker.ResolverOptions{
			Client: tracing.DefaultClient,
		}

		parsed, err := reference.ParseNormalizedNamed(ref)
		if err != nil {
			return def
		}
		host := reference.Domain(parsed)

		c, ok := m[host]
		if !ok {
			return def
		}

		if len(c.Mirrors) > 0 {
			def.Host = func(string) (string, error) {
				return c.Mirrors[rand.Intn(len(c.Mirrors))], nil
			}
		}

		if c.ExtraCA != "" {
			// if a CA is specified for this registry, add it to the trusted CA pool.
			rootCAs, _ := x509.SystemCertPool()
			if rootCAs == nil {
				rootCAs = x509.NewCertPool()
			}
			certs, err := ioutil.ReadFile(c.ExtraCA)
			if err != nil {
				logrus.Errorf("Failed to append %q to RootCAs: %v", c.ExtraCA, err)
			}
			if ok := rootCAs.AppendCertsFromPEM(certs); !ok {
				logrus.Infof("No certs appended, using system certs only")
			}
			config := &tls.Config{
				RootCAs: rootCAs,
			}
			def.Client = &http.Client{
				Transport: &tracing.Transport{
					RoundTripper: &nethttp.Transport{RoundTripper: &http.Transport{
						TLSClientConfig: config,
					}},
				},
			}
		}

		def.PlainHTTP = c.PlainHTTP

		return def
	}
}
