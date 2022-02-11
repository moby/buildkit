# Containerizing BuildKit

## Docker container

BuildKit can also be used by running the `buildkitd` daemon inside a Docker
container and accessing it remotely.

We provide the container images as [`moby/buildkit`](https://hub.docker.com/r/moby/buildkit/tags/):

- `moby/buildkit:latest`: built from the latest regular [release](https://github.com/moby/buildkit/releases)
- `moby/buildkit:rootless`: same as `latest` but runs as an unprivileged user, see [rootless mode](rootless-mode.md) docs.
- `moby/buildkit:master`: built from the master branch
- `moby/buildkit:master-rootless`: same as master but runs as an unprivileged user, see [rootless mode](rootless-mode.md) docs.

To run daemon in a container:

```shell
docker run -d --name buildkitd --privileged moby/buildkit:latest
export BUILDKIT_HOST=docker-container://buildkitd
buildctl build --help
```

## Podman

To connect to a BuildKit daemon running in a Podman container, use
`podman-container://` instead of `docker-container://` .

```shell
podman run -d --name buildkitd --privileged moby/buildkit:latest
buildctl --addr=podman-container://buildkitd build --frontend dockerfile.v0 --local context=. --local dockerfile=. --output type=oci | podman load foo
```

`sudo` is not required.

## Daemonless

To run the client and an ephemeral daemon in a single container ("daemonless mode"):

```shell
docker run \
  -it \
  --rm \
  --privileged \
  -v /path/to/dir:/tmp/work \
  --entrypoint buildctl-daemonless.sh \
  moby/buildkit:master \
    build \
    --frontend dockerfile.v0 \
    --local context=/tmp/work \
    --local dockerfile=/tmp/work
```

or

```shell
docker run \
  -it \
  --rm \
  --security-opt seccomp=unconfined \
  --security-opt apparmor=unconfined \
  -e BUILDKITD_FLAGS=--oci-worker-no-process-sandbox \
  -v /path/to/dir:/tmp/work \
  --entrypoint buildctl-daemonless.sh \
  moby/buildkit:master-rootless \
    build \
    --frontend \
    dockerfile.v0 \
    --local context=/tmp/work \
    --local dockerfile=/tmp/work
```
