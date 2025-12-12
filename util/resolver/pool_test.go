package resolver

import (
	"crypto/tls"
	"net/http"
	"testing"

	"github.com/containerd/containerd/v2/core/remotes/docker"
	"github.com/stretchr/testify/require"
)

func TestHostsFuncUsesLastClientForAuthorizer(t *testing.T) {
	// This test verifies that when multiple registry hosts are returned
	// (e.g., mirrors + actual registry), the authorizer uses the LAST
	// host's client, not the first. This is important for mTLS because
	// mirrors are added first in NewRegistryConfig, and the actual registry
	// (with proper TLS config) is added last.

	// Create distinct clients to track which one is used
	mirrorClient := &http.Client{Transport: &http.Transport{}}
	registryClient := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{
			// This simulates a client with mTLS certificates configured
			Certificates: []tls.Certificate{{}},
		},
	}}

	// Create mock hosts function that returns mirror first, actual registry last
	hosts := func(host string) ([]docker.RegistryHost, error) {
		return []docker.RegistryHost{
			{
				Host:   "mirror.example.com",
				Scheme: "https",
				Path:   "/v2",
				Client: mirrorClient,
			},
			{
				Host:   "registry.example.com",
				Scheme: "https",
				Path:   "/v2",
				Client: registryClient,
			},
		}, nil
	}

	// Create resolver with our mock hosts function
	pool := NewPool()
	resolver := pool.GetResolver(hosts, "registry.example.com/image:tag", "pull", nil, nil)

	// Call HostsFunc to get the configured hosts
	result, err := resolver.HostsFunc("registry.example.com")
	require.NoError(t, err)
	require.Len(t, result, 2)

	// Verify all hosts have the same authorizer
	require.NotNil(t, result[0].Authorizer)
	require.NotNil(t, result[1].Authorizer)
	require.Equal(t, result[0].Authorizer, result[1].Authorizer)

	// The authorizer should be using the LAST host's client (registryClient)
	// which has the TLS config with certificates, not the mirror's client.
	// We verify this by checking the dockerAuthorizer's client field.
	auth, ok := result[0].Authorizer.(*dockerAuthorizer)
	require.True(t, ok, "authorizer should be *dockerAuthorizer")

	// The client should be the registry client (last in list), not the mirror client
	require.Equal(t, registryClient, auth.client,
		"authorizer should use the last host's client (actual registry), not the first (mirror)")
}

func TestHostsFuncSingleHost(t *testing.T) {
	// Test that single host case still works correctly
	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{},
	}}

	hosts := func(host string) ([]docker.RegistryHost, error) {
		return []docker.RegistryHost{
			{
				Host:   "registry.example.com",
				Scheme: "https",
				Path:   "/v2",
				Client: client,
			},
		}, nil
	}

	pool := NewPool()
	resolver := pool.GetResolver(hosts, "registry.example.com/image:tag", "pull", nil, nil)

	result, err := resolver.HostsFunc("registry.example.com")
	require.NoError(t, err)
	require.Len(t, result, 1)

	auth, ok := result[0].Authorizer.(*dockerAuthorizer)
	require.True(t, ok)
	require.Equal(t, client, auth.client)
}
