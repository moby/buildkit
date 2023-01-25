# buildkitd.toml

The TOML file used to configure the buildkitd daemon settings has a short
list of global settings followed by a series of sections for specific areas
of daemon configuration.

The file path is `/etc/buildkit/buildkitd.toml` for rootful mode,
`~/.config/buildkit/buildkitd.toml` for rootless mode.

The following is a complete `buildkitd.toml` configuration example, please
note some configuration is only good for edge cases, please take care of it
carefully.

```toml
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
  gc = true
  gckeepstorage = 9000
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
  # gckeepstorage sets storage limit for default gc profile, in MB.
  gckeepstorage = 9000
  # maintain a pool of reusable CNI network namespaces to amortize the overhead
  # of allocating and releasing the namespaces
  cniPoolSize = 16

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
```
