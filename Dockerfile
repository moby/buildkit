# syntax=docker/dockerfile-upstream:1.4

ARG RUNC_VERSION=v1.1.3
ARG CONTAINERD_VERSION=v1.6.6
# containerd v1.5 for integration tests
ARG CONTAINERD_ALT_VERSION_15=v1.5.11
# containerd v1.4 for integration tests
ARG CONTAINERD_ALT_VERSION_14=v1.4.13
# BUILDKIT_TARGET defines buildkitd worker mode (buildkitd, buildkitd.oci_only, buildkitd.containerd_only)
ARG BUILDKIT_TARGET=buildkitd
ARG REGISTRY_VERSION=2.8.0
ARG ROOTLESSKIT_VERSION=v0.14.6
ARG CNI_VERSION=v1.1.0
ARG STARGZ_SNAPSHOTTER_VERSION=v0.12.0
ARG NERDCTL_VERSION=v0.17.1
ARG DNSNAME_VERSION=v1.3.1

# ALPINE_VERSION sets version for the base layers
ARG ALPINE_VERSION=3.15

# git stage is used for checking out remote repository sources
FROM --platform=$BUILDPLATFORM alpine:${ALPINE_VERSION} AS git
RUN apk add --no-cache git

# xx is a helper for cross-compilation
FROM --platform=$BUILDPLATFORM tonistiigi/xx@sha256:1e96844fadaa2f9aea021b2b05299bc02fe4c39a92d8e735b93e8e2b15610128 AS xx

FROM --platform=$BUILDPLATFORM golang:1.19-alpine AS golatest

# gobuild is base stage for compiling go/cgo
FROM golatest AS gobuild-base
RUN apk add --no-cache file bash clang lld pkgconfig git make
COPY --link --from=xx / /

# runc source
FROM git AS runc-src
ARG RUNC_VERSION
WORKDIR /usr/src
RUN git clone https://github.com/opencontainers/runc.git runc \
  && cd runc && git checkout -q "$RUNC_VERSION"

# build runc binary
FROM gobuild-base AS runc
WORKDIR $GOPATH/src/github.com/opencontainers/runc
ARG TARGETPLATFORM
# gcc is only installed for libgcc
# lld has issues building static binaries for ppc so prefer ld for it
RUN set -e; xx-apk add musl-dev gcc libseccomp-dev libseccomp-static; \
  [ "$(xx-info arch)" != "ppc64le" ] || XX_CC_PREFER_LINKER=ld xx-clang --setup-target-triple
RUN --mount=from=runc-src,src=/usr/src/runc,target=. --mount=target=/root/.cache,type=cache \
  CGO_ENABLED=1 xx-go build -mod=vendor -ldflags '-extldflags -static' -tags 'apparmor seccomp netgo cgo static_build osusergo' -o /usr/bin/runc ./ && \
  xx-verify --static /usr/bin/runc

# dnsname CNI plugin for testing
FROM gobuild-base AS dnsname
ARG DNSNAME_VERSION
WORKDIR /go/dnsname
RUN git clone https://github.com/containers/dnsname.git . \
  && git checkout -q "$DNSNAME_VERSION"
RUN --mount=target=/root/.cache,type=cache \
  set -e; make binaries; mv bin/dnsname /usr/bin/dnsname

FROM gobuild-base AS buildkit-base
WORKDIR /src
ENV GOFLAGS=-mod=vendor

# scan the version/revision info
FROM buildkit-base AS buildkit-version
# TODO: PKG should be inferred from go modules
RUN --mount=target=. \
  PKG=github.com/moby/buildkit VERSION=$(git describe --match 'v[0-9]*' --dirty='.m' --always --tags) REVISION=$(git rev-parse HEAD)$(if ! git diff --no-ext-diff --quiet --exit-code; then echo .m; fi); \
  echo "-X ${PKG}/version.Version=${VERSION} -X ${PKG}/version.Revision=${REVISION} -X ${PKG}/version.Package=${PKG}" | tee /tmp/.ldflags; \
  echo -n "${VERSION}" | tee /tmp/.version;

# build buildctl binary
FROM buildkit-base AS buildctl
ENV CGO_ENABLED=0
ARG TARGETPLATFORM
RUN --mount=target=. --mount=target=/root/.cache,type=cache \
  --mount=target=/go/pkg/mod,type=cache \
  --mount=source=/tmp/.ldflags,target=/tmp/.ldflags,from=buildkit-version \
  xx-go build -ldflags "$(cat /tmp/.ldflags)" -o /usr/bin/buildctl ./cmd/buildctl && \
  xx-verify --static /usr/bin/buildctl

# build buildkitd binary
FROM buildkit-base AS buildkitd
# BUILDKITD_TAGS defines additional Go build tags for compiling buildkitd
ARG BUILDKITD_TAGS
ARG TARGETPLATFORM
RUN --mount=target=. --mount=target=/root/.cache,type=cache \
  --mount=target=/go/pkg/mod,type=cache \
  --mount=source=/tmp/.ldflags,target=/tmp/.ldflags,from=buildkit-version \
  CGO_ENABLED=0 xx-go build -ldflags "$(cat /tmp/.ldflags) -extldflags '-static'" -tags "osusergo netgo static_build seccomp ${BUILDKITD_TAGS}" -o /usr/bin/buildkitd ./cmd/buildkitd && \
  xx-verify --static /usr/bin/buildkitd

FROM scratch AS binaries-linux-helper
COPY --link --from=runc /usr/bin/runc /buildkit-runc
# built from https://github.com/tonistiigi/binfmt/releases/tag/buildkit%2Fv7.0.0-29
COPY --link --from=tonistiigi/binfmt:buildkit-v7.0.0-29@sha256:5168f6c2b692b04a7c0102751220e293e109b3dc312eb22ee1a5547b42b58de6 / /
FROM binaries-linux-helper AS binaries-linux
COPY --link --from=buildctl /usr/bin/buildctl /
COPY --link --from=buildkitd /usr/bin/buildkitd /

FROM scratch AS binaries-darwin
COPY --link --from=buildctl /usr/bin/buildctl /

FROM scratch AS binaries-windows
COPY --link --from=buildctl /usr/bin/buildctl /buildctl.exe

FROM binaries-$TARGETOS AS binaries

FROM --platform=$BUILDPLATFORM alpine:${ALPINE_VERSION} AS releaser
RUN apk add --no-cache tar gzip
WORKDIR /work
ARG TARGETPLATFORM
RUN --mount=from=binaries \
  --mount=source=/tmp/.version,target=/tmp/.version,from=buildkit-version \
  mkdir -p /out && tar czvf "/out/buildkit-$(cat /tmp/.version).$(echo $TARGETPLATFORM | sed 's/\//-/g').tar.gz" --mtime='2015-10-21 00:00Z' --sort=name --transform 's/^./bin/' .

FROM scratch AS release
COPY --link --from=releaser /out/ /

# tonistiigi/alpine supports riscv64
FROM tonistiigi/alpine:${ALPINE_VERSION} AS buildkit-export
RUN apk add --no-cache fuse3 git openssh pigz xz \
  && ln -s fusermount3 /usr/bin/fusermount
COPY --link examples/buildctl-daemonless/buildctl-daemonless.sh /usr/bin/
VOLUME /var/lib/buildkit

FROM git AS containerd-src
ARG CONTAINERD_VERSION
ARG CONTAINERD_ALT_VERSION
WORKDIR /usr/src
RUN git clone https://github.com/containerd/containerd.git containerd

FROM gobuild-base AS containerd-base
WORKDIR /go/src/github.com/containerd/containerd
ARG TARGETPLATFORM
ENV CGO_ENABLED=1 BUILDTAGS=no_btrfs GO111MODULE=off
RUN xx-apk add musl-dev gcc && xx-go --wrap

FROM containerd-base AS containerd
ARG CONTAINERD_VERSION
RUN --mount=from=containerd-src,src=/usr/src/containerd,readwrite --mount=target=/root/.cache,type=cache \
  git fetch origin \
  && git checkout -q "$CONTAINERD_VERSION" \
  && make bin/containerd \
  && make bin/containerd-shim-runc-v2 \
  && make bin/ctr \
  && mv bin /out

# containerd v1.5 for integration tests
FROM containerd-base as containerd-alt-15
ARG CONTAINERD_ALT_VERSION_15
RUN --mount=from=containerd-src,src=/usr/src/containerd,readwrite --mount=target=/root/.cache,type=cache \
  git fetch origin \
  && git checkout -q "$CONTAINERD_ALT_VERSION_15" \
  && make bin/containerd \
  && make bin/containerd-shim-runc-v2 \
  && mv bin /out

# containerd v1.4 for integration tests
FROM containerd-base as containerd-alt-14
ARG CONTAINERD_ALT_VERSION_14
RUN --mount=from=containerd-src,src=/usr/src/containerd,readwrite --mount=target=/root/.cache,type=cache \
  git fetch origin \
  && git checkout -q "$CONTAINERD_ALT_VERSION_14" \
  && make bin/containerd \
  && make bin/containerd-shim-runc-v2 \
  && mv bin /out

ARG REGISTRY_VERSION
FROM registry:$REGISTRY_VERSION AS registry

FROM gobuild-base AS rootlesskit
ARG ROOTLESSKIT_VERSION
RUN git clone https://github.com/rootless-containers/rootlesskit.git /go/src/github.com/rootless-containers/rootlesskit
WORKDIR /go/src/github.com/rootless-containers/rootlesskit
ARG TARGETPLATFORM
RUN  --mount=target=/root/.cache,type=cache \
  git checkout -q "$ROOTLESSKIT_VERSION"  && \
  CGO_ENABLED=0 xx-go build -o /rootlesskit ./cmd/rootlesskit && \
  xx-verify --static /rootlesskit

FROM gobuild-base AS stargz-snapshotter
ARG STARGZ_SNAPSHOTTER_VERSION
RUN git clone https://github.com/containerd/stargz-snapshotter.git /go/src/github.com/containerd/stargz-snapshotter
WORKDIR /go/src/github.com/containerd/stargz-snapshotter
ARG TARGETPLATFORM
RUN --mount=target=/root/.cache,type=cache \
  git checkout -q "$STARGZ_SNAPSHOTTER_VERSION" && \
  xx-go --wrap && \
  mkdir /out && CGO_ENABLED=0 PREFIX=/out/ make && \
  xx-verify --static /out/containerd-stargz-grpc && \
  xx-verify --static /out/ctr-remote

# Copy together all binaries needed for oci worker mode
FROM buildkit-export AS buildkit-buildkitd.oci_only
COPY --link --from=buildkitd.oci_only /usr/bin/buildkitd.oci_only /usr/bin/
COPY --link --from=buildctl /usr/bin/buildctl /usr/bin/
ENTRYPOINT ["buildkitd.oci_only"]

# Copy together all binaries for containerd worker mode
FROM buildkit-export AS buildkit-buildkitd.containerd_only
COPY --link --from=buildkitd.containerd_only /usr/bin/buildkitd.containerd_only /usr/bin/
COPY --link --from=buildctl /usr/bin/buildctl /usr/bin/
ENTRYPOINT ["buildkitd.containerd_only"]

# Copy together all binaries for oci+containerd mode
FROM buildkit-export AS buildkit-buildkitd-linux
COPY --link --from=binaries / /usr/bin/
ENTRYPOINT ["buildkitd"]

FROM binaries AS buildkit-buildkitd-darwin

FROM binaries AS buildkit-buildkitd-windows
# this is not in binaries-windows because it is not intended for release yet, just CI
COPY --link --from=buildkitd /usr/bin/buildkitd /buildkitd.exe

FROM buildkit-buildkitd-$TARGETOS AS buildkit-buildkitd

FROM alpine:${ALPINE_VERSION} AS containerd-runtime
COPY --link --from=runc /usr/bin/runc /usr/bin/
COPY --link --from=containerd /out/containerd* /usr/bin/
COPY --link --from=containerd /out/ctr /usr/bin/
VOLUME /var/lib/containerd
VOLUME /run/containerd
ENTRYPOINT ["containerd"]

FROM --platform=$BUILDPLATFORM alpine:${ALPINE_VERSION} AS cni-plugins
RUN apk add --no-cache curl
ARG CNI_VERSION
ARG TARGETOS
ARG TARGETARCH
WORKDIR /opt/cni/bin
RUN curl -Ls https://github.com/containernetworking/plugins/releases/download/$CNI_VERSION/cni-plugins-$TARGETOS-$TARGETARCH-$CNI_VERSION.tgz | tar xzv
COPY --link --from=dnsname /usr/bin/dnsname /opt/cni/bin/

FROM buildkit-base AS integration-tests-base
ENV BUILDKIT_INTEGRATION_ROOTLESS_IDPAIR="1000:1000"
ARG NERDCTL_VERSION
RUN apk add --no-cache shadow shadow-uidmap sudo vim iptables ip6tables dnsmasq fuse curl \
  && useradd --create-home --home-dir /home/user --uid 1000 -s /bin/sh user \
  && echo "XDG_RUNTIME_DIR=/run/user/1000; export XDG_RUNTIME_DIR" >> /home/user/.profile \
  && mkdir -m 0700 -p /run/user/1000 \
  && chown -R user /run/user/1000 /home/user \
  && ln -s /sbin/iptables-legacy /usr/bin/iptables \
  && xx-go --wrap \
  && curl -Ls https://raw.githubusercontent.com/containerd/nerdctl/$NERDCTL_VERSION/extras/rootless/containerd-rootless.sh > /usr/bin/containerd-rootless.sh \
  && chmod 0755 /usr/bin/containerd-rootless.sh
# musl is needed to directly use the registry binary that is built on alpine
ENV BUILDKIT_INTEGRATION_CONTAINERD_EXTRA="containerd-1.4=/opt/containerd-alt-14/bin,containerd-1.5=/opt/containerd-alt-15/bin"
ENV BUILDKIT_INTEGRATION_SNAPSHOTTER=stargz
ENV CGO_ENABLED=0
COPY --link --from=stargz-snapshotter /out/* /usr/bin/
COPY --link --from=rootlesskit /rootlesskit /usr/bin/
COPY --link --from=containerd-alt-14 /out/containerd* /opt/containerd-alt-14/bin/
COPY --link --from=containerd-alt-15 /out/containerd* /opt/containerd-alt-15/bin/
COPY --link --from=registry /bin/registry /usr/bin/
COPY --link --from=runc /usr/bin/runc /usr/bin/
COPY --link --from=containerd /out/containerd* /usr/bin/
COPY --link --from=cni-plugins /opt/cni/bin/bridge /opt/cni/bin/host-local /opt/cni/bin/loopback /opt/cni/bin/firewall /opt/cni/bin/dnsname /opt/cni/bin/
COPY --link hack/fixtures/cni.json /etc/buildkit/cni.json
COPY --link hack/fixtures/dns-cni.conflist /etc/buildkit/dns-cni.conflist
COPY --link --from=binaries / /usr/bin/

# integration-tests prepares an image suitable for running all tests
FROM integration-tests-base AS integration-tests
COPY . .
ENV BUILDKIT_RUN_NETWORK_INTEGRATION_TESTS=1 BUILDKIT_CNI_INIT_LOCK_PATH=/run/buildkit_cni_bridge.lock

FROM integration-tests AS dev-env
VOLUME /var/lib/buildkit

# Rootless mode.
FROM tonistiigi/alpine:${ALPINE_VERSION} AS rootless
RUN apk add --no-cache fuse3 fuse-overlayfs git openssh pigz shadow-uidmap xz
RUN adduser -D -u 1000 user \
  && mkdir -p /run/user/1000 /home/user/.local/tmp /home/user/.local/share/buildkit \
  && chown -R user /run/user/1000 /home/user \
  && echo user:100000:65536 | tee /etc/subuid | tee /etc/subgid
COPY --link --from=rootlesskit /rootlesskit /usr/bin/
COPY --link --from=binaries / /usr/bin/
COPY --link examples/buildctl-daemonless/buildctl-daemonless.sh /usr/bin/
# Kubernetes runAsNonRoot requires USER to be numeric
USER 1000:1000
ENV HOME /home/user
ENV USER user
ENV XDG_RUNTIME_DIR=/run/user/1000
ENV TMPDIR=/home/user/.local/tmp
ENV BUILDKIT_HOST=unix:///run/user/1000/buildkit/buildkitd.sock
VOLUME /home/user/.local/share/buildkit
ENTRYPOINT ["rootlesskit", "buildkitd"]

# buildkit builds the buildkit container image
FROM buildkit-${BUILDKIT_TARGET} AS buildkit
