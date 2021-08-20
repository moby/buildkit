package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConfig(t *testing.T) {

	const testConfig = `
root = "/foo/bar"
debug=true
insecure-entitlements = ["security.insecure"]

[gc]
enabled=true


[grpc]
address=["buildkit.sock"]
debugAddress="debug.sock"
gid=1234
[grpc.tls]
cert="mycert.pem"

[worker.oci]
enabled=true
snapshotter="overlay"
rootless=true
gc=false
gckeepstorage=123456789
[worker.oci.labels]
foo="bar"
"aa.bb.cc"="baz"

[worker.containerd]
namespace="non-default"
platforms=["linux/amd64"]
address="containerd.sock"
[[worker.containerd.gcpolicy]]
all=true
filters=["foo==bar"]
keepBytes=20
keepDuration=3600
[[worker.containerd.gcpolicy]]
keepBytes=40
keepDuration=7200

[registry."docker.io"]
mirrors=["hub.docker.io"]
http=true
insecure=true
ca=["myca.pem"]
tlsconfigdir=["/etc/buildkitd/myregistry"]
[[registry."docker.io".keypair]]
key="key.pem"
cert="cert.pem"

[dns]
nameservers=["1.1.1.1","8.8.8.8"]
options=["edns0"]
searchDomains=["example.com"]
`

	cfg, err := Load(bytes.NewBuffer([]byte(testConfig)))
	require.NoError(t, err)

	require.Equal(t, "/foo/bar", cfg.Root)
	require.Equal(t, true, cfg.Debug)
	require.Equal(t, "security.insecure", cfg.Entitlements[0])

	require.Equal(t, "buildkit.sock", cfg.GRPC.Address[0])
	require.Equal(t, "debug.sock", cfg.GRPC.DebugAddress)
	require.Nil(t, cfg.GRPC.UID)
	require.NotNil(t, cfg.GRPC.GID)
	require.Equal(t, 1234, *cfg.GRPC.GID)
	require.Equal(t, "mycert.pem", cfg.GRPC.TLS.Cert)

	require.NotNil(t, cfg.Workers.OCI.Enabled)
	require.Equal(t, int64(123456789), cfg.Workers.OCI.GCKeepStorage)
	require.Equal(t, true, *cfg.Workers.OCI.Enabled)
	require.Equal(t, "overlay", cfg.Workers.OCI.Snapshotter)
	require.Equal(t, true, cfg.Workers.OCI.Rootless)
	require.Equal(t, false, *cfg.Workers.OCI.GC)

	require.Equal(t, "bar", cfg.Workers.OCI.Labels["foo"])
	require.Equal(t, "baz", cfg.Workers.OCI.Labels["aa.bb.cc"])

	require.Nil(t, cfg.Workers.Containerd.Enabled)
	require.Equal(t, 1, len(cfg.Workers.Containerd.Platforms))
	require.Equal(t, "containerd.sock", cfg.Workers.Containerd.Address)

	require.Equal(t, 0, len(cfg.Workers.OCI.GCPolicy))
	require.Equal(t, "non-default", cfg.Workers.Containerd.Namespace)
	require.Equal(t, 2, len(cfg.Workers.Containerd.GCPolicy))

	require.Nil(t, cfg.Workers.Containerd.GC)
	require.Equal(t, true, cfg.Workers.Containerd.GCPolicy[0].All)
	require.Equal(t, false, cfg.Workers.Containerd.GCPolicy[1].All)
	require.Equal(t, int64(20), cfg.Workers.Containerd.GCPolicy[0].KeepBytes)
	require.Equal(t, int64(40), cfg.Workers.Containerd.GCPolicy[1].KeepBytes)
	require.Equal(t, int64(3600), cfg.Workers.Containerd.GCPolicy[0].KeepDuration)
	require.Equal(t, int64(7200), cfg.Workers.Containerd.GCPolicy[1].KeepDuration)
	require.Equal(t, 1, len(cfg.Workers.Containerd.GCPolicy[0].Filters))
	require.Equal(t, 0, len(cfg.Workers.Containerd.GCPolicy[1].Filters))

	require.Equal(t, *cfg.Registries["docker.io"].PlainHTTP, true)
	require.Equal(t, *cfg.Registries["docker.io"].Insecure, true)
	require.Equal(t, cfg.Registries["docker.io"].Mirrors[0], "hub.docker.io")
	require.Equal(t, cfg.Registries["docker.io"].RootCAs, []string{"myca.pem"})
	require.Equal(t, cfg.Registries["docker.io"].TLSConfigDir, []string{"/etc/buildkitd/myregistry"})
	require.Equal(t, cfg.Registries["docker.io"].KeyPairs[0].Key, "key.pem")
	require.Equal(t, cfg.Registries["docker.io"].KeyPairs[0].Certificate, "cert.pem")

	require.NotNil(t, cfg.DNS)
	require.Equal(t, cfg.DNS.Nameservers, []string{"1.1.1.1", "8.8.8.8"})
	require.Equal(t, cfg.DNS.SearchDomains, []string{"example.com"})
	require.Equal(t, cfg.DNS.Options, []string{"edns0"})
}
