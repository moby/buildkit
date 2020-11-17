# syntax = docker/dockerfile:1.1-experimental

# TODO update RUNC_VERSION with next release after 1.0-rc92
ARG RUNC_VERSION=939ad4e3fcfa1ab531458355a73688c6f4ee5003
ARG CONTAINERD_VERSION=v1.4.0
# containerd v1.3 for integration tests
ARG CONTAINERD_ALT_VERSION=v1.3.7
# available targets: buildkitd, buildkitd.oci_only, buildkitd.containerd_only
ARG BUILDKIT_TARGET=buildkitd
ARG REGISTRY_VERSION=2.7.1
ARG ROOTLESSKIT_VERSION=v0.11.0
ARG CNI_VERSION=v0.8.6
ARG SHADOW_VERSION=4.8.1
ARG FUSEOVERLAYFS_VERSION=v1.2.0
ARG STARGZ_SNAPSHOTTER_VERSION=3a04e4c2c116c85b4b66d01945cf7ebcb7a2eb5a

ARG ALPINE_VERSION=3.12

FROM alpine:${ALPINE_VERSION} AS alpine

# git stage is used for checking out remote repository sources
FROM --platform=$BUILDPLATFORM alpine AS git
RUN apk add --no-cache git xz

# xgo is a helper for golang cross-compilation
FROM --platform=$BUILDPLATFORM tonistiigi/xx:golang@sha256:6f7d999551dd471b58f70716754290495690efa8421e0a1fcf18eb11d0c0a537 AS xgo

# gobuild is base stage for compiling go/cgo
FROM --platform=$BUILDPLATFORM golang:1.13-buster AS gobuild-minimal
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
  apt-get --no-install-recommends install -y \
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
RUN git clone https://github.com/opencontainers/runc.git runc \
  && cd runc && git checkout -q "$RUNC_VERSION"

# build runc binary
FROM gobuild-base AS runc
WORKDIR $GOPATH/src/github.com/opencontainers/runc
ARG TARGETPLATFORM
RUN --mount=from=runc-src,src=/usr/src/runc,target=. --mount=target=/root/.cache,type=cache \
  CGO_ENABLED=1 go build -ldflags '-extldflags -static' -tags 'seccomp netgo cgo static_build osusergo' -o /usr/bin/runc ./ && \
  file /usr/bin/runc | grep "statically linked"

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
  set -x; go build -ldflags "$(cat /tmp/.ldflags)" -o /usr/bin/buildctl ./cmd/buildctl && \
  file /usr/bin/buildctl && file /usr/bin/buildctl | egrep "statically linked|Mach-O|Windows"

# build buildkitd binary
FROM buildkit-base AS buildkitd
ARG TARGETPLATFORM
ARG BUILDKITD_TAGS
RUN --mount=target=. --mount=target=/root/.cache,type=cache \
  --mount=target=/go/pkg/mod,type=cache \
  --mount=source=/tmp/.ldflags,target=/tmp/.ldflags,from=buildkit-version \
  go build -ldflags "$(cat /tmp/.ldflags) -extldflags '-static'" -tags "osusergo netgo static_build seccomp ${BUILDKITD_TAGS}" -o /usr/bin/buildkitd ./cmd/buildkitd && \
  file /usr/bin/buildkitd | egrep "statically linked|Windows"

FROM scratch AS binaries-linux-helper
COPY --from=runc /usr/bin/runc /buildkit-runc
# built from https://github.com/tonistiigi/binfmt/tree/85394e2a1bf0ac9e6c291945e869322bea969445/binfmt
COPY --from=tonistiigi/binfmt:buildkit / /
FROM binaries-linux-helper AS binaries-linux
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

FROM git AS buildkit-export
RUN apk add --no-cache fuse3 pigz && ln -s fusermount3 /usr/bin/fusermount
COPY examples/buildctl-daemonless/buildctl-daemonless.sh /usr/bin/
VOLUME /var/lib/buildkit

FROM git AS containerd-src
ARG CONTAINERD_VERSION
ARG CONTAINERD_ALT_VERSION
WORKDIR /usr/src
RUN git clone https://github.com/containerd/containerd.git containerd

FROM gobuild-base AS containerd-base
RUN apt-get --no-install-recommends install -y btrfs-progs libbtrfs-dev
WORKDIR /go/src/github.com/containerd/containerd

FROM containerd-base AS containerd
ARG CONTAINERD_VERSION
RUN --mount=from=containerd-src,src=/usr/src/containerd,readwrite --mount=target=/root/.cache,type=cache \
  git fetch origin \
  && git checkout -q "$CONTAINERD_VERSION" \
  && make bin/containerd \
  && make bin/containerd-shim-runc-v2 \
  && make bin/ctr \
  && mv bin /out

# containerd v1.3 for integration tests
FROM containerd-base as containerd-alt
ARG CONTAINERD_ALT_VERSION
RUN --mount=from=containerd-src,src=/usr/src/containerd,readwrite --mount=target=/root/.cache,type=cache \
  git fetch origin \
  && git checkout -q "$CONTAINERD_ALT_VERSION" \
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
  CGO_ENABLED=0 go build -o /rootlesskit ./cmd/rootlesskit && \
  file /rootlesskit | grep "statically linked"

FROM gobuild-base AS stargz-snapshotter
ARG STARGZ_SNAPSHOTTER_VERSION
RUN git clone https://github.com/containerd/stargz-snapshotter.git /go/src/github.com/containerd/stargz-snapshotter
WORKDIR /go/src/github.com/containerd/stargz-snapshotter
RUN --mount=target=/root/.cache,type=cache \
  git checkout -q "$STARGZ_SNAPSHOTTER_VERSION" && \
  mkdir /out && CGO_ENABLED=0 PREFIX=/out/ make && \
  file /out/containerd-stargz-grpc | grep "statically linked" && \
  file /out/ctr-remote | grep "statically linked"

FROM --platform=$BUILDPLATFORM alpine AS fuse-overlayfs
RUN apk add --no-cache curl
ARG FUSEOVERLAYFS_VERSION
ARG TARGETARCH
RUN echo $TARGETARCH | sed -e s/^amd64$/x86_64/ -e s/^arm64$/aarch64/ -e s/^arm$/armv7l/ > /uname_m && \
  mkdir /out && \
  curl -sSL -o /out/fuse-overlayfs https://github.com/containers/fuse-overlayfs/releases/download/${FUSEOVERLAYFS_VERSION}/fuse-overlayfs-$(cat /uname_m) && \
  chmod +x /out/fuse-overlayfs

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
FROM buildkit-export AS buildkit-buildkitd-linux
COPY --from=binaries / /usr/bin/
ENTRYPOINT ["buildkitd"]

FROM binaries AS buildkit-buildkitd-darwin

FROM binaries AS buildkit-buildkitd-windows
# this is not in binaries-windows because it is not intended for release yet, just CI
COPY --from=buildkitd /usr/bin/buildkitd /buildkitd.exe

FROM buildkit-buildkitd-$TARGETOS AS buildkit-buildkitd

FROM alpine AS containerd-runtime
COPY --from=runc /usr/bin/runc /usr/bin/
COPY --from=containerd /out/containerd* /usr/bin/
COPY --from=containerd /out/ctr /usr/bin/
VOLUME /var/lib/containerd
VOLUME /run/containerd
ENTRYPOINT ["containerd"]

FROM --platform=$BUILDPLATFORM alpine AS cni-plugins
RUN apk add --no-cache curl
ARG CNI_VERSION
ARG TARGETOS
ARG TARGETARCH
WORKDIR /opt/cni/bin
RUN curl -Ls https://github.com/containernetworking/plugins/releases/download/$CNI_VERSION/cni-plugins-$TARGETOS-$TARGETARCH-$CNI_VERSION.tgz | tar xzv

FROM buildkit-base AS integration-tests-base
ENV BUILDKIT_INTEGRATION_ROOTLESS_IDPAIR="1000:1000"
RUN apt-get --no-install-recommends install -y uidmap sudo vim iptables fuse \
  && useradd --create-home --home-dir /home/user --uid 1000 -s /bin/sh user \
  && echo "XDG_RUNTIME_DIR=/run/user/1000; export XDG_RUNTIME_DIR" >> /home/user/.profile \
  && mkdir -m 0700 -p /run/user/1000 \
  && chown -R user /run/user/1000 /home/user \
  && update-alternatives --set iptables /usr/sbin/iptables-legacy
# musl is needed to directly use the registry binary that is built on alpine
ENV BUILDKIT_INTEGRATION_CONTAINERD_EXTRA="containerd-1.3=/opt/containerd-alt/bin"
ENV BUILDKIT_INTEGRATION_SNAPSHOTTER=stargz
COPY --from=stargz-snapshotter /out/* /usr/bin/
COPY --from=rootlesskit /rootlesskit /usr/bin/
COPY --from=containerd-alt /out/containerd* /opt/containerd-alt/bin/
COPY --from=registry /bin/registry /usr/bin
COPY --from=runc /usr/bin/runc /usr/bin
COPY --from=containerd /out/containerd* /usr/bin/
COPY --from=cni-plugins /opt/cni/bin/bridge /opt/cni/bin/host-local /opt/cni/bin/loopback /opt/cni/bin/
COPY hack/fixtures/cni.json /etc/buildkit/cni.json
COPY --from=binaries / /usr/bin/

FROM integration-tests-base AS integration-tests
COPY . .
ENV BUILDKIT_RUN_NETWORK_INTEGRATION_TESTS=1 BUILDKIT_CNI_INIT_LOCK_PATH=/run/buildkit_cni_bridge.lock

FROM integration-tests AS dev-env
VOLUME /var/lib/buildkit

# newuidmap & newgidmap binaries (shadow-uidmap 4.7-r1) shipped with alpine cannot be executed without CAP_SYS_ADMIN,
# because the binaries are built without libcap-dev.
# So we need to build the binaries with libcap enabled.
FROM alpine AS idmap
RUN apk add --no-cache autoconf automake build-base byacc gettext gettext-dev gcc git libcap-dev libtool libxslt
RUN git clone https://github.com/shadow-maint/shadow.git /shadow
WORKDIR /shadow
ARG SHADOW_VERSION
RUN git checkout $SHADOW_VERSION
RUN ./autogen.sh --disable-nls --disable-man --without-audit --without-selinux --without-acl --without-attr --without-tcb --without-nscd \
  && make \
  && cp src/newuidmap src/newgidmap /usr/bin

# Rootless mode.
FROM alpine AS rootless
RUN apk add --no-cache fuse3 git xz pigz
COPY --from=idmap /usr/bin/newuidmap /usr/bin/newuidmap
COPY --from=idmap /usr/bin/newgidmap /usr/bin/newgidmap
COPY --from=fuse-overlayfs /out/fuse-overlayfs /usr/bin/
# we could just set CAP_SETUID filecap rather than `chmod u+s`, but requires kernel >= 4.14
RUN chmod u+s /usr/bin/newuidmap /usr/bin/newgidmap \
  && adduser -D -u 1000 user \
  && mkdir -p /run/user/1000 /home/user/.local/tmp /home/user/.local/share/buildkit \
  && chown -R user /run/user/1000 /home/user \
  && echo user:100000:65536 | tee /etc/subuid | tee /etc/subgid
COPY --from=rootlesskit /rootlesskit /usr/bin/
COPY --from=binaries / /usr/bin/
COPY examples/buildctl-daemonless/buildctl-daemonless.sh /usr/bin/
# Kubernetes runAsNonRoot requires USER to be numeric
USER 1000:1000
ENV HOME /home/user
ENV USER user
ENV XDG_RUNTIME_DIR=/run/user/1000
ENV TMPDIR=/home/user/.local/tmp
ENV BUILDKIT_HOST=unix:///run/user/1000/buildkit/buildkitd.sock
VOLUME /home/user/.local/share/buildkit
ENTRYPOINT ["rootlesskit", "buildkitd"]


FROM buildkit-${BUILDKIT_TARGET}


