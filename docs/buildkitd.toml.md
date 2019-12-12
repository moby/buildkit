# /etc/buildkit/buildkitd.toml

## NAME

buildkitd.toml - configuration file for buildkitd


## DESCRIPTION

The TOML file used to configure the buildkitd daemon settings has a short
list of global settings followed by a series of sections for specific areas
of daemon configuration.

## EXAMPLE

The following is a complete **buildkitd.toml** configuration example,
please note some of the configuration is only good for edge cases, please
take care of it carefully.

```
debug = true
# root is where all buildkit state is stored.
root = "/var/lib/buildkit"
# insecure-entitlements allows insecure entitlements, disabled by default.
insecure-entitlements = [ "network.host", "security.insecure" ]

[grpc]
  address = [ "tcp://0.0.0.0:1234" ]
  # debugAddress is address for attaching go profiles and debuggers.
  debugAddress = "0.0.0.0:6060"
  uid = 0
  gid = 0
  [grpc.tls]
    cert = "/etc/buildkit/tls.crt"
    key = "/etc/buildkit/tls.key"
    ca = "/etc/buildkit/tlsca.crt"

[worker.oci]
  enabled = true
  # platforms is manually configure platforms, detected automatically if unset.
  platforms = [ "linux/amd64", "linux/arm64" ]
  snapshotter = "auto" # overlayfs or native, default value is "auto".
  rootless = false # see docs/rootless.md for the details on rootless mode.
  # Whether run subprocesses in main pid namespace or not, this is useful for
  # running rootless buildkit inside a container.
  noProcessSandbox = false
  gc = true
  gckeepstorage = 9000
  # alternate OCI worker binary name(example 'crun'), by default either 
  # buildkit-runc or runc binary is used
  binary = ""
  [worker.oci.labels]
    "foo" = "bar"

  [[worker.oci.gcpolicy]]
    keepBytes = 512000000
    keepDuration = 172800
    filters = [ "type==source.local", "type==exec.cachemount", "type==source.git.checkout"]
  [[worker.oci.gcpolicy]]
    all = true
    keepBytes = 1024000000

[worker.containerd]
  address = "/run/containerd/containerd.sock"
  enabled = true
  platforms = [ "linux/amd64", "linux/arm64" ]
  namespace = "buildkit"
  gc = true
  # gckeepstorage sets storage limit for default gc profile, in bytes.
  gckeepstorage = 9000
  [worker.containerd.labels]
    "foo" = "bar"

  [[worker.containerd.gcpolicy]]
    keepBytes = 512000000
    keepDuration = 172800 # in seconds
    filters = [ "type==source.local", "type==exec.cachemount", "type==source.git.checkout"]
  [[worker.containerd.gcpolicy]]
    all = true
    keepBytes = 1024000000

# registry configures a new Docker register used for cache import or output.
[registry."docker.io"]
  mirrors = ["hub.docker.io"]
  http = true
```
