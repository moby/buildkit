# Quick start

!!! tip
    For Kubernetes deployments, see the [Kubernetes examples](examples/kubernetes.md).

BuildKit is composed of the `buildkitd` daemon and the `buildctl` client.
While the `buildctl` client is available for Linux, macOS, and Windows, the
`buildkitd` daemon is only available for Linux currently.

The `buildkitd` daemon requires the following components to be installed:

- [runc](https://github.com/opencontainers/runc) or [crun](https://github.com/containers/crun)
- [containerd](https://github.com/containerd/containerd) (if you want to use containerd worker)

The latest binaries of BuildKit are available [here](https://github.com/moby/buildkit/releases)
for Linux, macOS, and Windows.

[Homebrew package](https://formulae.brew.sh/formula/buildkit) (unofficial) is
available for macOS:
```shell
brew install buildkit
```

To build BuildKit from source, see the [Contributing page](community/contributing.md).

## Starting the `buildkitd` daemon

You need to run `buildkitd` as the root user on the host.

```shell
sudo buildkitd
```

To run `buildkitd` as a non-root user, see the [rootless mode guide](user-guides/rootless-mode.md).

The buildkitd daemon supports two worker backends: OCI (runc) and containerd.

By default, the OCI (runc) worker is used. You can set `--oci-worker=false --containerd-worker=true`
to use the containerd worker.

We are open to adding more backends.

## Systemd socket activation

To start the buildkitd daemon using systemd socket activiation, you can install
the buildkit systemd unit files.

On Systemd based systems, you can communicate with the daemon via [Systemd socket activation](http://0pointer.de/blog/projects/socket-activation.html),
use `buildkitd --addr fd://`.

!!! tip
    You can find examples of using Systemd socket activation with BuildKit and Systemd in [`./examples/systemd`](https://github.com/moby/buildkit/tree/master/examples/systemd).

## Expose BuildKit as a TCP service

The buildkitd daemon listens gRPC API on `/run/buildkit/buildkitd.sock` by
default, but you can also use TCP sockets.

It is highly recommended to create TLS certificates for both the daemon and the
client (mTLS). Enabling TCP without mTLS is dangerous because the executor
containers (aka Dockerfile `RUN` containers) can call BuildKit API as well.

```shell
buildkitd \
  --addr tcp://0.0.0.0:1234 \
  --tlscacert /path/to/ca.pem \
  --tlscert /path/to/cert.pem \
  --tlskey /path/to/key.pem
```

```shell
buildctl \
  --addr tcp://example.com:1234 \
  --tlscacert /path/to/ca.pem \
  --tlscert /path/to/clientcert.pem \
  --tlskey /path/to/clientkey.pem \
  build ...
```

### Load balancing

`buildctl build` can be called against randomly load balanced the `buildkitd` daemon.

!!! tip
    If you have multiple BuildKit daemon instances, but you don't want to use
    registry for sharing cache across the cluster, consider client-side load
    balancing using consistent hashing.

    See [Consistent hashing example](examples/consistent-hashing.md) for more info.

## Exploring LLB

BuildKit builds are based on a binary intermediate format called LLB that is used for defining the dependency graph for processes running part of your build. tl;dr: LLB is to Dockerfile what LLVM IR is to C.

- Marshaled as Protobuf messages
- Concurrently executable
- Efficiently cacheable
- Vendor-neutral (i.e. non-Dockerfile languages can be easily implemented)

See [`solver/pb/ops.proto`](https://github.com/moby/buildkit/blob/master/solver/pb/ops.proto)
for the format definition, and see [our examples](examples/overview.md) for LLB
applications.

Currently, the following high-level languages has been implemented for LLB:

- Dockerfile (See [Dockerfile frontend docs](frontend/dockerfile.md))
- [Buildpacks](https://github.com/tonistiigi/buildkit-pack)
- [Mockerfile](https://matt-rickard.com/building-a-new-dockerfile-frontend/)
- [Gockerfile](https://github.com/po3rin/gockerfile)
- [bldr (Pkgfile)](https://github.com/talos-systems/bldr/)
- [HLB](https://github.com/openllb/hlb)
- [Earthfile (Earthly)](https://github.com/earthly/earthly)
- [Cargo Wharf (Rust)](https://github.com/denzp/cargo-wharf)
- [Nix](https://github.com/AkihiroSuda/buildkit-nix)
- (open a PR to add your own language)
