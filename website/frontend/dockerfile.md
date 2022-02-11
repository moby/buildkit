# Dockerfile frontend

## About

External versions of the Dockerfile frontend are pushed to [`docker/dockerfile-upstream`](https://hub.docker.com/r/docker/dockerfile-upstream)
and [`docker/dockerfile-upstream`](https://hub.docker.com/r/docker/dockerfile)
and can be used with the gateway frontend.

The source for the external frontend is currently located in [`./frontend/dockerfile`](https://github.com/moby/buildkit/tree/master/frontend/dockerfile)
but will move out of this repository in the future ([moby/buildkit#163](https://github.com/moby/buildkit/issues/163)).

For automatic build from master branch of this repository `docker/dockerfile-upstream:master`
or `docker/dockerfile-upstream:master-labs` image can be used.

!!! note "Note for Docker users"
    If you are using Docker v18.09 or later, BuildKit mode can be enabled by setting `export DOCKER_BUILDKIT=1` on the client side.

    [Docker Buildx](https://github.com/docker/buildx) always enables BuildKit.

## Build a Dockerfile with an external Dockerfile frontend 

```shell
buildctl build \
    --frontend gateway.v0 \
    --opt source=docker/dockerfile \
    --local context=. \
    --local dockerfile=.
```
```shell
buildctl build \
    --frontend gateway.v0 \
    --opt source=docker/dockerfile \
    --opt context=https://github.com/moby/moby.git \
    --opt build-arg:APT_MIRROR=cdn-fastly.deb.debian.org
```

## Using an external Dockerfile frontend

BuildKit supports loading frontends dynamically from container images. Images
for Dockerfile frontends are available at [`docker/dockerfile`](https://hub.docker.com/r/docker/dockerfile/tags/) repository.

To use the external frontend, the first line of your Dockerfile needs to be
`# syntax=docker/dockerfile:1.3` pointing to the specific image you want to use.

BuildKit also ships with Dockerfile frontend builtin, but it is recommended to
use an external image to make sure that all users use the same version on the
builder and to pick up bugfixes automatically without waiting for a new version
of BuildKit or Docker engine.

The images are published on two channels: *latest* and *labs*. The latest
channel uses semver versioning while labs uses an [incrementing number](https://github.com/moby/buildkit/issues/528).
This means the labs channel may remove a feature without incrementing the major
component of a version, and you may want to pin the image to a specific revision.
Even when syntaxes change in between releases on labs channel, the old versions
are guaranteed to be backward compatible.

{!../frontend/dockerfile/docs/syntax.md!}
