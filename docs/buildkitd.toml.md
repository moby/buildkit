---
title: buildkitd.toml
---

The TOML file used to configure the buildkitd daemon settings has a short
list of global settings followed by a series of sections for specific areas
of daemon configuration.

The file path is `/etc/buildkit/buildkitd.toml` for rootful mode,
`~/.config/buildkit/buildkitd.toml` for rootless mode.

The following is a complete `buildkitd.toml` configuration example.
Note that some configuration options are only useful in edge cases.

```toml
# debug enables additional debug logging
debug = true
# trace enables additional trace logging (very verbose, with potential performance impacts)
trace = true
# root is where all buildkit state is stored.
root = "/var/lib/buildkit"
# insecure-entitlements allows insecure entitlements, disabled by default.
insecure-entitlements = [ "network.host", "security.insecure" ]

[log]
  # log formatter: json or text
  format = "text"

[dns]
  nameservers=["1.1.1.1","8.8.8.8"]
  options=["edns0"]
  searchDomains=["example.com"]

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

[otel]
  # OTEL collector trace socket path
  socketPath = "/run/buildkit/otel-grpc.sock"

[cdi]
  # Disables support of the Container Device Interface (CDI).
  disabled = true
  # List of directories to scan for CDI spec files. For more details about CDI
  # specification, please refer to https://github.com/cncf-tags/container-device-interface/blob/main/SPEC.md#cdi-json-specification
  specDirs = ["/etc/cdi", "/var/run/cdi", "/etc/buildkit/cdi"]

# config for build history API that stores information about completed build commands
[history]
  # maxAge is the maximum age of history entries to keep, in seconds.
  maxAge = 172800
  # maxEntries is the maximum number of history entries to keep.
  maxEntries = 50

[worker.oci]
  enabled = true
  # platforms is manually configure platforms, detected automatically if unset.
  platforms = [ "linux/amd64", "linux/arm64" ]
  snapshotter = "auto" # overlayfs or native, default value is "auto".
  rootless = false # see docs/rootless.md for the details on rootless mode.
  # Whether run subprocesses in main pid namespace or not, this is useful for
  # running rootless buildkit inside a container.
  noProcessSandbox = false

  # gc enables/disables garbage collection
  gc = true
  # reservedSpace is the minimum amount of disk space guaranteed to be
  # retained by this buildkit worker - any usage below this threshold will not
  # be reclaimed during garbage collection.
  # all disk space parameters can be an integer number of bytes (e.g.
  # 512000000), a string with a unit (e.g. "512MB"), or a string percentage
  # of the total disk space (e.g. "10%")
  reservedSpace = "30%"
  # maxUsedSpace is the maximum amount of disk space that may be used by
  # this buildkit worker - any usage above this threshold will be reclaimed
  # during garbage collection.
  maxUsedSpace = "60%"
  # minFreeSpace is the target amount of free disk space that the garbage
  # collector will attempt to leave - however, it will never be bought below
  # reservedSpace.
  minFreeSpace = "20GB"

  # alternate OCI worker binary name(example 'crun'), by default either 
  # buildkit-runc or runc binary is used
  binary = ""
  # name of the apparmor profile that should be used to constrain build containers.
  # the profile should already be loaded (by a higher level system) before creating a worker.
  apparmor-profile = ""
  # limit the number of parallel build steps that can run at the same time
  max-parallelism = 4
  # maintain a pool of reusable CNI network namespaces to amortize the overhead
  # of allocating and releasing the namespaces
  cniPoolSize = 16

  [worker.oci.labels]
    "foo" = "bar"

  [[worker.oci.gcpolicy]]
    # reservedSpace is the minimum amount of disk space guaranteed to be
    # retained by this policy - any usage below this threshold will not be
    # reclaimed during # garbage collection.
    reservedSpace = "512MB"
    # maxUsedSpace is the maximum amount of disk space that may be used by this
    # policy - any usage above this threshold will be reclaimed during garbage
    # collection.
    maxUsedSpace = "1GB"
    # minFreeSpace is the target amount of free disk space that the garbage
    # collector will attempt to leave - however, it will never be bought below
    # reservedSpace.
    minFreeSpace = "10GB"

    # keepDuration can be an integer number of seconds (e.g. 172800), or a
    # string duration (e.g. "48h")
    keepDuration = "48h"
    filters = [ "type==source.local", "type==exec.cachemount", "type==source.git.checkout"]
  [[worker.oci.gcpolicy]]
    all = true
    reservedSpace = 1024000000

[worker.containerd]
  address = "/run/containerd/containerd.sock"
  enabled = true
  platforms = [ "linux/amd64", "linux/arm64" ]
  namespace = "buildkit"

  # gc enables/disables garbage collection
  gc = true
  # reservedSpace is the minimum amount of disk space guaranteed to be
  # retained by this buildkit worker - any usage below this threshold will not
  # be reclaimed during garbage collection.
  # all disk space parameters can be an integer number of bytes (e.g.
  # 512000000), a string with a unit (e.g. "512MB"), or a string percentage
  # of the total disk space (e.g. "10%")
  reservedSpace = "30%"
  # maxUsedSpace is the maximum amount of disk space that may be used by
  # this buildkit worker - any usage above this threshold will be reclaimed
  # during garbage collection.
  maxUsedSpace = "60%"
  # minFreeSpace is the target amount of free disk space that the garbage
  # collector will attempt to leave - however, it will never be bought below
  # reservedSpace.
  minFreeSpace = "20GB"

  # maintain a pool of reusable CNI network namespaces to amortize the overhead
  # of allocating and releasing the namespaces
  cniPoolSize = 16
  # defaultCgroupParent sets the parent cgroup of all containers.
  defaultCgroupParent = "buildkit"

  [worker.containerd.labels]
    "foo" = "bar"

  # configure the containerd runtime
  [worker.containerd.runtime]
    name = "io.containerd.runc.v2"
    path = "/path/to/containerd/runc/shim"
    options = { BinaryName = "runc" }

  [[worker.containerd.gcpolicy]]
    reservedSpace = 512000000
    keepDuration = 172800
    filters = [ "type==source.local", "type==exec.cachemount", "type==source.git.checkout"]
  [[worker.containerd.gcpolicy]]
    all = true
    reservedSpace = 1024000000

# registry configures a new Docker register used for cache import or output.
[registry."docker.io"]
  # mirror configuration to handle path in case a mirror registry requires a /project path rather than just a host:port
  mirrors = ["yourmirror.local:5000", "core.harbor.domain/proxy.docker.io"]
  http = true
  insecure = true
  ca=["/etc/config/myca.pem"]
  [[registry."docker.io".keypair]]
    key="/etc/config/key.pem"
    cert="/etc/config/cert.pem"

# optionally mirror configuration can be done by defining it as a registry.
[registry."yourmirror.local:5000"]
  http = true

# Frontend control
[frontend."dockerfile.v0"]
  enabled = true

[frontend."gateway.v0"]
  enabled = true

  # If allowedRepositories is empty, all gateway sources are allowed.
  # Otherwise, only the listed repositories are allowed as a gateway source.
  # 
  # NOTE: Only the repository name (without tag) is compared.
  #
  # Example:
  # allowedRepositories = [ "docker-registry.wikimedia.org/repos/releng/blubber/buildkit" ]
  allowedRepositories = []

[system]
  # how often buildkit scans for changes in the supported emulated platforms
  platformsCacheMaxAge = "1h"

```
