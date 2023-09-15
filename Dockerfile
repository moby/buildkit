# syntax=docker/dockerfile-upstream:master

ARG RUNC_VERSION=v1.1.9
ARG CONTAINERD_VERSION=v1.7.2
# containerd v1.6 for integration tests
ARG CONTAINERD_ALT_VERSION_16=v1.6.21
ARG REGISTRY_VERSION=2.8.0
ARG ROOTLESSKIT_VERSION=v1.0.1
ARG CNI_VERSION=v1.2.0
ARG STARGZ_SNAPSHOTTER_VERSION=v0.14.3
ARG NERDCTL_VERSION=v1.4.0
ARG DNSNAME_VERSION=v1.3.1
ARG NYDUS_VERSION=v2.1.6
ARG MINIO_VERSION=RELEASE.2022-05-03T20-36-08Z
ARG MINIO_MC_VERSION=RELEASE.2022-05-04T06-07-55Z
ARG AZURITE_VERSION=3.18.0
ARG GOTESTSUM_VERSION=v1.9.0

ARG GO_VERSION=1.20
ARG ALPINE_VERSION=3.18

# ALPINE_CACHE defaults to the "alpine-cache-local" stage in this image.
# Can be overridden to a custom image for reproducible builds.
ARG ALPINE_CACHE=alpine-cache-local

# minio for s3 integration tests
FROM minio/minio:${MINIO_VERSION} AS minio
FROM minio/mc:${MINIO_MC_VERSION} AS minio-mc

# alpine base for buildkit image
# TODO: remove this when alpine image supports riscv64
FROM --platform=amd64   alpine:${ALPINE_VERSION} AS alpine-amd64
FROM --platform=arm     alpine:${ALPINE_VERSION} AS alpine-arm
FROM --platform=arm64   alpine:${ALPINE_VERSION} AS alpine-arm64
FROM --platform=s390x   alpine:${ALPINE_VERSION} AS alpine-s390x
FROM --platform=ppc64le alpine:${ALPINE_VERSION} AS alpine-ppc64le
FROM --platform=riscv64 alpine:edge@sha256:2d01a16bab53a8405876cec4c27235d47455a7b72b75334c614f2fb0968b3f90 AS alpine-riscv64
FROM alpine-$TARGETARCH AS alpinebase
FROM --platform=$BUILDPLATFORM alpine AS alpinebase-buildplatform

# xx is a helper for cross-compilation
FROM --platform=$BUILDPLATFORM tonistiigi/xx:1.2.1 AS xx

FROM alpinebase AS alpine-cache-local-targetplatform
RUN --mount=source=./apklist,target=/apklist \
  mkdir -p /etc/apk/cache && apk update && apk cache download --available --add-dependencies $(grep -v '^#' /apklist)

FROM alpinebase-buildplatform AS alpine-cache-local-buildplatform
RUN --mount=source=./apklist,target=/apklist \
  mkdir -p /etc/apk/cache && apk update && apk cache download --available --add-dependencies $(grep -v '^#' /apklist)

FROM scratch AS alpine-cache-local
COPY --from=alpine-cache-local-targetplatform /etc/apk/cache /etc/apk/cache

# alpine-cache is the stage to collect apk cache files.
# This stage can be pushed for sake of reproducible builds.
FROM $ALPINE_CACHE AS alpine-cache
FROM --platform=amd64   alpine-cache AS alpine-cache-amd64
FROM --platform=arm     alpine-cache AS alpine-cache-arm
FROM --platform=arm64   alpine-cache AS alpine-cache-arm64
FROM --platform=s390x   alpine-cache AS alpine-cache-s390x
FROM --platform=ppc64le alpine-cache AS alpine-cache-ppc64le
FROM --platform=riscv64 alpine-cache AS alpine-cache-riscv64
FROM --platform=$TARGETPLATFORM alpine-cache-${TARGETARCH} AS alpine-cache-targetplatform
# NOTE: when $ALPINE_CACHE is set to alpine-cache-local, alpine-cache-remote-buildplatform is invalid,
# as alpine-cache-${BUILDARCH} is built from the TARGETPLATFORM image
FROM --platform=$BUILDPLATFORM  alpine-cache-${BUILDARCH}  AS alpine-cache-remote-buildplatform

FROM --platform=$BUILDPLATFORM alpinebase-buildplatform AS alpine-cache-xx
ARG BUILDPLATFORM
COPY --link --from=alpine-cache-remote-buildplatform /etc/apk/cache /etc/apk/cache/_remote_buildplatform
COPY --link --from=alpine-cache-local-buildplatform /etc/apk/cache /etc/apk/cache/_local_buildplatform
ARG ALPINE_CACHE
RUN <<EOT
if [ "$ALPINE_CACHE" = "alpine-cache-local" ]; then
  ln -sf /etc/apk/cache/_local_buildplatform/* /etc/apk/cache/
else
  ln -sf /etc/apk/cache/_remote_buildplatform/* /etc/apk/cache/
fi
EOT
ARG TARGETPLATFORM
COPY --link --from=alpine-cache-targetplatform /etc/apk/cache /etc/apk/cache/_targetplatform
COPY --link --from=xx / /
RUN ln -s _targetplatform /etc/apk/cache/_$(xx-info pkg-arch)

# go base image
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine${ALPINE_VERSION} AS golatest

# git stage is used for checking out remote repository sources
FROM --platform=$BUILDPLATFORM alpine:${ALPINE_VERSION} AS git
RUN --mount=from=alpine-cache-xx,source=/etc/apk/cache,target=/etc/apk/cache,rw \
  apk add --no-network git

# gobuild is base stage for compiling go/cgo
FROM golatest AS gobuild-base
RUN --mount=from=alpine-cache-xx,source=/etc/apk/cache,target=/etc/apk/cache,rw \
  apk add --no-network file bash clang lld musl-dev pkgconfig git make
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
RUN --mount=from=alpine-cache-xx,source=/etc/apk/cache,target=/etc/apk/cache,rw \
  set -e; xx-apk add --no-network musl-dev gcc libseccomp-dev libseccomp-static; \
  [ "$(xx-info arch)" != "ppc64le" ] || XX_CC_PREFER_LINKER=ld xx-clang --setup-target-triple
RUN --mount=from=runc-src,src=/usr/src/runc,target=. --mount=target=/root/.cache,type=cache \
  CGO_ENABLED=1 xx-go build -mod=vendor -ldflags '-extldflags -static' -tags 'apparmor seccomp netgo cgo static_build osusergo' -o /usr/bin/runc ./ && \
  xx-verify --static /usr/bin/runc

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
ARG GOBUILDFLAGS
ARG VERIFYFLAGS="--static"
ARG CGO_ENABLED=0
RUN --mount=target=. --mount=target=/root/.cache,type=cache \
  --mount=target=/go/pkg/mod,type=cache \
  --mount=source=/tmp/.ldflags,target=/tmp/.ldflags,from=buildkit-version \
  xx-go build ${GOBUILDFLAGS} -ldflags "$(cat /tmp/.ldflags) -extldflags '-static'" -tags "osusergo netgo static_build seccomp ${BUILDKITD_TAGS}" -o /usr/bin/buildkitd ./cmd/buildkitd && \
  xx-verify ${VERIFYFLAGS} /usr/bin/buildkitd

FROM scratch AS binaries-linux
COPY --link --from=runc /usr/bin/runc /buildkit-runc
# built from https://github.com/tonistiigi/binfmt/releases/tag/buildkit%2Fv7.1.0-30
COPY --link --from=tonistiigi/binfmt:buildkit-v7.1.0-30@sha256:45dd57b4ba2f24e2354f71f1e4e51f073cb7a28fd848ce6f5f2a7701142a6bf0 / /
COPY --link --from=buildctl /usr/bin/buildctl /
COPY --link --from=buildkitd /usr/bin/buildkitd /

FROM scratch AS binaries-darwin
COPY --link --from=buildctl /usr/bin/buildctl /

FROM scratch AS binaries-windows
COPY --link --from=buildctl /usr/bin/buildctl /buildctl.exe

FROM scratch AS binaries-freebsd
COPY --link --from=buildkitd /usr/bin/buildkitd /
COPY --link --from=buildctl /usr/bin/buildctl /

FROM binaries-$TARGETOS AS binaries
# enable scanning for this stage
ARG BUILDKIT_SBOM_SCAN_STAGE=true

FROM --platform=$BUILDPLATFORM alpine:${ALPINE_VERSION} AS releaser
RUN apk add --no-cache tar gzip
WORKDIR /work
ARG TARGETPLATFORM
RUN --mount=from=binaries \
  --mount=source=/tmp/.version,target=/tmp/.version,from=buildkit-version \
  mkdir -p /out && tar czvf "/out/buildkit-$(cat /tmp/.version).$(echo $TARGETPLATFORM | sed 's/\//-/g').tar.gz" --mtime='2015-10-21 00:00Z' --sort=name --transform 's/^./bin/' .

FROM scratch AS release
COPY --link --from=releaser /out/ /

FROM alpinebase AS buildkit-export
RUN --mount=from=alpine-cache-targetplatform,source=/etc/apk/cache,target=/etc/apk/cache,rw \
  apk add --no-network fuse3 git openssh pigz xz \
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
RUN --mount=from=alpine-cache-xx,source=/etc/apk/cache,target=/etc/apk/cache,rw \
  xx-apk add --no-network musl-dev gcc && xx-go --wrap

FROM containerd-base AS containerd
ARG CONTAINERD_VERSION
RUN --mount=from=containerd-src,src=/usr/src/containerd,readwrite --mount=target=/root/.cache,type=cache \
  git fetch origin \
  && git checkout -q "$CONTAINERD_VERSION" \
  && make bin/containerd \
  && make bin/containerd-shim-runc-v2 \
  && make bin/ctr \
  && mv bin /out

# containerd v1.6 for integration tests
FROM containerd-base as containerd-alt-16
ARG CONTAINERD_ALT_VERSION_16
RUN --mount=from=containerd-src,src=/usr/src/containerd,readwrite --mount=target=/root/.cache,type=cache \
  git fetch origin \
  && git checkout -q "$CONTAINERD_ALT_VERSION_16" \
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

FROM gobuild-base AS nydus
ARG NYDUS_VERSION
ARG TARGETOS
ARG TARGETARCH
SHELL ["/bin/bash", "-c"]
RUN wget https://github.com/dragonflyoss/image-service/releases/download/$NYDUS_VERSION/nydus-static-$NYDUS_VERSION-$TARGETOS-$TARGETARCH.tgz
RUN mkdir -p /out/nydus-static && tar xzvf nydus-static-$NYDUS_VERSION-$TARGETOS-$TARGETARCH.tgz -C /out

FROM gobuild-base AS gotestsum
ARG GOTESTSUM_VERSION
RUN --mount=target=/root/.cache,type=cache \
  GOBIN=/out/ go install "gotest.tools/gotestsum@${GOTESTSUM_VERSION}" && \
  /out/gotestsum --version

FROM buildkit-export AS buildkit-linux
COPY --link --from=binaries / /usr/bin/
ENV BUILDKIT_SETUP_CGROUPV2_ROOT=1
ENTRYPOINT ["buildkitd"]

FROM binaries AS buildkit-darwin

FROM binaries AS buildkit-freebsd
ENTRYPOINT ["/buildkitd"]

FROM binaries AS buildkit-windows
# this is not in binaries-windows because it is not intended for release yet, just CI
COPY --link --from=buildkitd /usr/bin/buildkitd /buildkitd.exe

# dnsname source
FROM git AS dnsname-src
ARG DNSNAME_VERSION
WORKDIR /usr/src
RUN git clone https://github.com/containers/dnsname.git dnsname \
  && cd dnsname && git checkout -q "$DNSNAME_VERSION"

# build dnsname CNI plugin for testing
FROM gobuild-base AS dnsname
WORKDIR /go/src/github.com/containers/dnsname
ARG TARGETPLATFORM
RUN --mount=from=dnsname-src,src=/usr/src/dnsname,target=.,rw \
    --mount=target=/root/.cache,type=cache \
    CGO_ENABLED=0 xx-go build -o /usr/bin/dnsname ./plugins/meta/dnsname && \
    xx-verify --static /usr/bin/dnsname

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
RUN --mount=from=alpine-cache-xx,source=/etc/apk/cache,target=/etc/apk/cache,rw \
  apk add --no-network shadow shadow-uidmap sudo vim iptables ip6tables dnsmasq fuse curl git-daemon openssh-client \
  && useradd --create-home --home-dir /home/user --uid 1000 -s /bin/sh user \
  && echo "XDG_RUNTIME_DIR=/run/user/1000; export XDG_RUNTIME_DIR" >> /home/user/.profile \
  && mkdir -m 0700 -p /run/user/1000 \
  && chown -R user /run/user/1000 /home/user \
  && ln -s /sbin/iptables-legacy /usr/bin/iptables \
  && xx-go --wrap
ARG NERDCTL_VERSION
RUN curl -Ls https://raw.githubusercontent.com/containerd/nerdctl/$NERDCTL_VERSION/extras/rootless/containerd-rootless.sh > /usr/bin/containerd-rootless.sh \
  && chmod 0755 /usr/bin/containerd-rootless.sh
ARG AZURITE_VERSION
RUN --mount=from=alpine-cache-xx,source=/etc/apk/cache,target=/etc/apk/cache,rw \
  apk add --no-network nodejs npm \
  && npm install -g azurite@${AZURITE_VERSION}
# The entrypoint script is needed for enabling nested cgroup v2 (https://github.com/moby/buildkit/issues/3265#issuecomment-1309631736)
RUN curl -Ls https://raw.githubusercontent.com/moby/moby/v20.10.21/hack/dind > /docker-entrypoint.sh \
  && chmod 0755 /docker-entrypoint.sh
ENTRYPOINT ["/docker-entrypoint.sh"]
# musl is needed to directly use the registry binary that is built on alpine
ENV BUILDKIT_INTEGRATION_CONTAINERD_EXTRA="containerd-1.6=/opt/containerd-alt-16/bin"
ENV BUILDKIT_INTEGRATION_SNAPSHOTTER=stargz
ENV BUILDKIT_SETUP_CGROUPV2_ROOT=1
ENV CGO_ENABLED=0
ENV GOTESTSUM_FORMAT=standard-verbose
COPY --link --from=gotestsum /out/gotestsum /usr/bin/
COPY --link --from=minio /opt/bin/minio /usr/bin/
COPY --link --from=minio-mc /usr/bin/mc /usr/bin/
COPY --link --from=nydus /out/nydus-static/* /usr/bin/
COPY --link --from=stargz-snapshotter /out/* /usr/bin/
COPY --link --from=rootlesskit /rootlesskit /usr/bin/
COPY --link --from=containerd-alt-16 /out/containerd* /opt/containerd-alt-16/bin/
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
FROM alpinebase AS rootless
RUN --mount=from=alpine-cache-targetplatform,source=/etc/apk/cache,target=/etc/apk/cache,rw \
  apk add --no-network fuse3 fuse-overlayfs git openssh pigz shadow-uidmap xz
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
FROM buildkit-$TARGETOS AS buildkit
