package resolver

import (
	"bytes"
	"path"
	"testing"

	"github.com/containerd/containerd/v2/core/remotes/docker"
	"github.com/moby/buildkit/cmd/buildkitd/config"
	"github.com/stretchr/testify/require"
)

func TestNewMirrorRegistryHost(t *testing.T) {
	const testConfig = `
[registry."docker.io"]
mirrors = ["hub.docker.io", "yourmirror.local:5000/proxy.docker.io"]
[registry."quay.io"]
mirrors = ["yourmirror.local:5000/proxy.quay.io"]
[registry."fake.io"]
mirrors = ["https://url/", "https://url/path/"]
`

	tests := map[string]struct {
		description string
		host        string
		path        string
	}{
		"hub.docker.io": {
			description: "docker_io_mirror_without_path",
			host:        "hub.docker.io",
			path:        defaultPath,
		},
		"yourmirror.local:5000/proxy.docker.io": {
			description: "docker_io_mirror_with_path",
			host:        "yourmirror.local:5000",
			path:        path.Join(defaultPath, "proxy.docker.io"),
		},
		"yourmirror.local:5000/proxy.quay.io": {
			description: "docker_quay_mirror_with_path",
			host:        "yourmirror.local:5000",
			path:        path.Join(defaultPath, "proxy.quay.io"),
		},
		"https://url/": {
			description: "docker_fake_mirror_scheme_without_path",
			host:        "url",
			path:        defaultPath,
		},
		"https://url/path/": {
			description: "docker_fake_mirror_scheme_with_path",
			host:        "url",
			path:        path.Join(defaultPath, "path"),
		},
	}

	cfg, err := config.Load(bytes.NewBuffer([]byte(testConfig)))
	require.NoError(t, err)

	require.NotEqual(t, 0, len(cfg.Registries))
	for _, registry := range cfg.Registries {
		require.NotEqual(t, 0, len(registry.Mirrors))
		for _, m := range registry.Mirrors {
			test := tests[m]
			h := newMirrorRegistryHost(m)
			require.NotNil(t, h)
			require.Equal(t, h.Host, test.host)
			require.Equal(t, h.Path, test.path)
		}
	}
}

func TestNewRegistryConfig(t *testing.T) {
	const testConfig = `
[registry."docker.io"]
mirrors = ["yourmirror.local", "proxy.local:5000/proxy.docker.io"]

[registry."yourmirror.local"]
http = true

[registry."proxy.local:5000"]
capabilities = ["pull", "resolve", "push"]
`

	pull, resolve, push := docker.HostCapabilityPull, docker.HostCapabilityResolve, docker.HostCapabilityPush
	tests := map[string][]struct {
		host         string
		scheme       string
		path         string
		capabilities docker.HostCapabilities
	}{
		"docker.io": {
			{
				host:         "yourmirror.local",
				scheme:       "http",
				path:         defaultPath,
				capabilities: pull | resolve,
			},
			{
				host:         "proxy.local:5000",
				scheme:       "https",
				path:         path.Join(defaultPath, "proxy.docker.io"),
				capabilities: pull | resolve | push,
			},
			{
				host:         "registry-1.docker.io",
				scheme:       "https",
				path:         defaultPath,
				capabilities: pull | resolve | push,
			},
		},
		"yourmirror.local": {
			{
				host:         "yourmirror.local",
				scheme:       "http",
				path:         defaultPath,
				capabilities: pull | resolve | push,
			},
		},
	}

	cfg, err := config.Load(bytes.NewBuffer([]byte(testConfig)))
	require.NoError(t, err)

	require.NotEqual(t, 0, len(cfg.Registries))
	registryHosts := NewRegistryConfig(cfg.Registries)
	require.NotNil(t, registryHosts)

	for hostname, testHost := range tests {
		hosts, err := registryHosts(hostname)
		require.NoError(t, err)
		require.Equal(t, len(testHost), len(hosts))
		for i, host := range hosts {
			test := testHost[i]
			require.Equal(t, test.host, host.Host)
			require.Equal(t, test.capabilities, host.Capabilities)
			require.Equal(t, test.scheme, host.Scheme)
			require.Equal(t, test.path, host.Path)
		}
	}
}
