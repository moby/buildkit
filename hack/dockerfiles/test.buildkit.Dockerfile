# syntax = tonistiigi/dockerfile:runmount20180925

ARG RUNC_VERSION=00dc70017d222b178a002ed30e9321b12647af2d
ARG CONTAINERD_VERSION=v1.1.3
# containerd v1.0 for integration tests
ARG CONTAINERD10_VERSION=v1.0.3
# available targets: buildkitd, buildkitd.oci_only, buildkitd.containerd_only
ARG BUILDKIT_TARGET=buildkitd
ARG REGISTRY_VERSION=2.6
ARG ROOTLESSKIT_VERSION=4f7ae4607d626f0a22fb495056d55b17cce8c01b
ARG ROOTLESS_BASE_MODE=external

# git stage is used for checking out remote repository sources
FROM --platform=$BUILDPLATFORM alpine AS git
RUN apk add --no-cache git

# xgo is a helper for golang cross-compilation
FROM --platform=$BUILDPLATFORM tonistiigi/xx:golang@sha256:6f7d999551dd471b58f70716754290495690efa8421e0a1fcf18eb11d0c0a537 AS xgo

# gobuild is base stage for compiling go/cgo
FROM --platform=$BUILDPLATFORM golang:1.11 AS gobuild-minimal
COPY --from=xgo / /
RUN apt-get update && apt-get install --no-install-recommends -y libseccomp-dev file

# on amd64 you can also cross-compile to other platforms
FROM gobuild-minimal AS gobuild-cross-amd64
RUN dpkg --add-architecture s390x && \
  dpkg --add-architecture ppc64el && \
  dpkg --add-architecture armel && \
  dpkg --add-architecture armhf && \
  dpkg --add-architecture arm64 && \
  apt-get update && \
  apt-get install -y \
  gcc-s390x-linux-gnu libc6-dev-s390x-cross libseccomp-dev:s390x \
  crossbuild-essential-ppc64el libseccomp-dev:ppc64el \
  crossbuild-essential-armel libseccomp-dev:armel \
  crossbuild-essential-armhf libseccomp-dev:armhf \
  crossbuild-essential-arm64 libseccomp-dev:arm64 \
  --no-install-recommends

# define all valid target configurations for compilation
FROM gobuild-minimal AS gobuild-amd64-amd64
FROM gobuild-minimal AS gobuild-arm-arm
FROM gobuild-minimal AS gobuild-s390x-s390x
FROM gobuild-minimal AS gobuild-ppc64le-ppc64le
FROM gobuild-minimal AS gobuild-arm64-arm64
FROM gobuild-cross-amd64 AS gobuild-amd64-arm
FROM gobuild-cross-amd64 AS gobuild-amd64-s390x
FROM gobuild-cross-amd64 AS gobuild-amd64-ppc64le
FROM gobuild-cross-amd64 AS gobuild-amd64-arm64
FROM gobuild-$BUILDARCH-$TARGETARCH AS gobuild-base

# runc source
FROM git AS runc-src
ARG RUNC_VERSION
WORKDIR /usr/src
RUN git clone git://github.com/opencontainers/runc.git runc \
	&& cd runc && git checkout -q "$RUNC_VERSION"

# build runc binary
FROM gobuild-base AS runc
WORKDIR $GOPATH/src/github.com/opencontainers/runc
ARG TARGETPLATFORM
RUN --mount=from=runc-src,src=/usr/src/runc,target=. --mount=target=/root/.cache,type=cache \
  CGO_ENABLED=1 go build -ldflags '-w -extldflags -static' -tags 'seccomp netgo cgo static_build osusergo' -o /usr/bin/runc ./ && \
  file /usr/bin/runc | grep "statically linked"

FROM gobuild-base AS buildkit-base
WORKDIR /go/src/github.com/moby/buildkit

# scan the version/revision info
FROM buildkit-base AS buildkit-version
RUN --mount=target=. \
  PKG=github.com/moby/buildkit VERSION=$(git describe --match 'v[0-9]*' --dirty='.m' --always --tags) REVISION=$(git rev-parse HEAD)$(if ! git diff --no-ext-diff --quiet --exit-code; then echo .m; fi); \
  echo "-X ${PKG}/version.Version=${VERSION} -X ${PKG}/version.Revision=${REVISION} -X ${PKG}/version.Package=${PKG}" | tee /tmp/.ldflags; \
  echo -n "${VERSION}" | tee /tmp/.version;

# build buildctl binary 
FROM buildkit-base AS buildctl
ENV CGO_ENABLED=0
ARG TARGETPLATFORM
RUN --mount=target=. --mount=target=/root/.cache,type=cache \
  --mount=source=/tmp/.ldflags,target=/tmp/.ldflags,from=buildkit-version \
  set -x; go build -ldflags "$(cat /tmp/.ldflags)" -o /usr/bin/buildctl ./cmd/buildctl && \
  file /usr/bin/buildctl && file /usr/bin/buildctl | egrep "statically linked|Mach-O|Windows"

# build buildkitd binary
FROM buildkit-base AS buildkitd
ENV CGO_ENABLED=1
ARG TARGETPLATFORM
RUN --mount=target=. --mount=target=/root/.cache,type=cache \
  --mount=source=/tmp/.ldflags,target=/tmp/.ldflags,from=buildkit-version \
  go build -ldflags "$(cat /tmp/.ldflags) -w -extldflags -static" -tags 'osusergo seccomp netgo cgo static_build ' -o /usr/bin/buildkitd ./cmd/buildkitd && \
  file /usr/bin/buildkitd | grep "statically linked"

FROM scratch AS binaries-linux
COPY --from=runc /usr/bin/runc /buildkit-runc
COPY --from=buildctl /usr/bin/buildctl /
COPY --from=buildkitd /usr/bin/buildkitd /

FROM scratch AS binaries-darwin
COPY --from=buildctl /usr/bin/buildctl /

FROM scratch AS binaries-windows
COPY --from=buildctl /usr/bin/buildctl /buildctl.exe

FROM binaries-$TARGETOS AS binaries

FROM --platform=$BUILDPLATFORM alpine AS releaser
RUN apk add --no-cache tar gzip
WORKDIR /work
ARG TARGETPLATFORM
RUN --mount=from=binaries \
  --mount=source=/tmp/.version,target=/tmp/.version,from=buildkit-version \
  mkdir -p /out && tar czvf "/out/buildkit-$(cat /tmp/.version).$(echo $TARGETPLATFORM | sed 's/\//-/g').tar.gz" --mtime='2015-10-21 00:00Z' --sort=name --transform 's/^./bin/' .

FROM scratch AS release
COPY --from=releaser /out/ /

FROM tonistiigi/git@sha256:704fcc24a17b40833625ee37c4a4acf0e4aa90d0aa276926d63847097134defd AS buildkit-export
VOLUME /var/lib/buildkit

FROM git AS containerd-src
ARG CONTAINERD_VERSION
WORKDIR /usr/src
RUN git clone https://github.com/containerd/containerd.git containerd

FROM gobuild-base AS containerd-base
RUN apt-get install -y --no-install-recommends btrfs-tools btrfs-progs
WORKDIR /go/src/github.com/containerd/containerd

FROM containerd-base AS containerd
ARG CONTAINERD_VERSION
RUN --mount=from=containerd-src,src=/usr/src/containerd,readwrite --mount=target=/root/.cache,type=cache \
  git fetch origin \
  && git checkout -q "$CONTAINERD_VERSION" \
  && make bin/containerd \
  && make bin/containerd-shim \
  && make bin/ctr \
  && mv bin /out

# containerd v1.0 for integration tests
FROM containerd-base as containerd10
ARG CONTAINERD10_VERSION
RUN --mount=from=containerd-src,src=/usr/src/containerd,readwrite --mount=target=/root/.cache,type=cache \
  git fetch origin \
  && git checkout -q "$CONTAINERD10_VERSION" \
  && make bin/containerd \
  && make bin/containerd-shim \
  && mv bin /out

FROM registry:$REGISTRY_VERSION AS registry

FROM gobuild-base AS rootlesskit
ARG ROOTLESSKIT_VERSION
RUN git clone https://github.com/rootless-containers/rootlesskit.git /go/src/github.com/rootless-containers/rootlesskit
WORKDIR /go/src/github.com/rootless-containers/rootlesskit
ARG TARGETPLATFORM
RUN  --mount=target=/root/.cache,type=cache \
  git checkout -q "$ROOTLESSKIT_VERSION"  && \
  CGO_ENABLED=0 go build -o /rootlesskit ./cmd/rootlesskit && \
  file /rootlesskit | grep "statically linked"

# Copy together all binaries needed for oci worker mode
FROM buildkit-export AS buildkit-buildkitd.oci_only
COPY --from=buildkitd.oci_only /usr/bin/buildkitd.oci_only /usr/bin/
COPY --from=buildctl /usr/bin/buildctl /usr/bin/
ENTRYPOINT ["buildkitd.oci_only"]

# Copy together all binaries for containerd worker mode
FROM buildkit-export AS buildkit-buildkitd.containerd_only
COPY --from=buildkitd.containerd_only /usr/bin/buildkitd.containerd_only /usr/bin/
COPY --from=buildctl /usr/bin/buildctl /usr/bin/
ENTRYPOINT ["buildkitd.containerd_only"]

# Copy together all binaries for oci+containerd mode
FROM buildkit-export AS buildkit-buildkitd
COPY --from=binaries / /usr/bin/
ENTRYPOINT ["buildkitd"]

FROM alpine AS containerd-runtime
COPY --from=runc /usr/bin/runc /usr/bin/
COPY --from=containerd /out/containerd* /usr/bin/
COPY --from=containerd /out/ctr /usr/bin/
VOLUME /var/lib/containerd
VOLUME /run/containerd
ENTRYPOINT ["containerd"]

FROM buildkit-base AS integration-tests
ENV BUILDKIT_INTEGRATION_ROOTLESS_IDPAIR="1000:1000"
RUN apt-get install -y --no-install-recommends uidmap sudo musl \ 
  && useradd --create-home --home-dir /home/user --uid 1000 -s /bin/sh user \
  && echo "XDG_RUNTIME_DIR=/run/user/1000; export XDG_RUNTIME_DIR" >> /home/user/.profile \
  && mkdir -m 0700 -p /run/user/1000 \
  && chown -R user /run/user/1000 /home/user
  # musl is needed to directly use the registry binary that is built on alpine
ENV BUILDKIT_INTEGRATION_CONTAINERD_EXTRA="containerd-1.0=/opt/containerd-1.0/bin"
COPY --from=rootlesskit /rootlesskit /usr/bin/
COPY --from=containerd10 /out/containerd* /opt/containerd-1.0/bin/
COPY --from=registry /bin/registry /usr/bin
COPY --from=runc /usr/bin/runc /usr/bin
COPY --from=containerd /out/containerd* /usr/bin/
COPY --from=binaries / /usr/bin/
COPY . .

FROM alpine AS rootless-base-internal
RUN apk add --no-cache git shadow shadow-uidmap \
  && useradd --create-home --home-dir /home/user --uid 1000 user \
  && mkdir -p /run/user/1000 /home/user/.local/tmp /home/user/.local/share/buildkit \
  && chown -R user /run/user/1000 /home/user \
  && rm /bin/su && ln -s /bin/busybox /bin/su

# tonistiigi/buildkit:rootless-base is a pre-built multi-arch version of rootless-base-internal https://github.com/moby/buildkit/pull/666#pullrequestreview-161872350
FROM tonistiigi/buildkit:rootless-base@sha256:6d9c50e2d006c2a8745e9d7f2bc075e4469191eccada41936ec0c6070361d45a AS rootless-base-external
FROM rootless-base-$ROOTLESS_BASE_MODE AS rootless-base

# Rootless mode.
# Still requires `--privileged`.
FROM rootless-base AS rootless
COPY --from=rootlesskit /rootlesskit /usr/bin/
COPY --from=binaries / /usr/bin/
USER user
ENV HOME /home/user
ENV USER user
ENV XDG_RUNTIME_DIR=/run/user/1000
ENV TMPDIR=/home/user/.local/tmp
VOLUME /home/user/.local/share/buildkit
ENTRYPOINT ["rootlesskit", "buildkitd"]


FROM buildkit-${BUILDKIT_TARGET}


