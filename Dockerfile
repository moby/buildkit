# syntax=docker/dockerfile-upstream:master

ARG RUNC_VERSION=v1.2.2
ARG CONTAINERD_VERSION=v2.0.0
# CONTAINERD_ALT_VERSION_... defines fallback containerd version for integration tests
ARG CONTAINERD_ALT_VERSION_17=v1.7.23
ARG CONTAINERD_ALT_VERSION_16=v1.6.36
ARG REGISTRY_VERSION=v2.8.3
ARG ROOTLESSKIT_VERSION=v2.3.1
ARG CNI_VERSION=v1.5.1
ARG STARGZ_SNAPSHOTTER_VERSION=v0.15.1
ARG NERDCTL_VERSION=v1.6.2
ARG DNSNAME_VERSION=v1.3.1
ARG NYDUS_VERSION=v2.2.4
ARG MINIO_VERSION=RELEASE.2022-05-03T20-36-08Z
ARG MINIO_MC_VERSION=RELEASE.2022-05-04T06-07-55Z
ARG AZURITE_VERSION=3.18.0
ARG GOTESTSUM_VERSION=v1.9.0
ARG DELVE_VERSION=v1.23.1

ARG GO_VERSION=1.23
ARG ALPINE_VERSION=3.20
ARG XX_VERSION=1.5.0
ARG BUILDKIT_DEBUG

# minio for s3 integration tests
FROM minio/minio:${MINIO_VERSION} AS minio
FROM minio/mc:${MINIO_MC_VERSION} AS minio-mc

# xx is a helper for cross-compilation
FROM --platform=$BUILDPLATFORM tonistiigi/xx:${XX_VERSION} AS xx

# golatest is alias for Go base image
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine${ALPINE_VERSION} AS golatest

# gobuild-base is base stage for compiling go/cgo
FROM golatest AS gobuild-base
RUN apk add --no-cache file bash clang lld musl-dev pkgconfig git make
COPY --link --from=xx / /

# runc builds runc binary
FROM gobuild-base AS runc
WORKDIR $GOPATH/src/github.com/opencontainers/runc
ARG RUNC_VERSION
ADD --keep-git-dir=true "https://github.com/opencontainers/runc.git#$RUNC_VERSION" .
ARG TARGETPLATFORM
# gcc is only installed for libgcc
# lld has issues building static binaries for ppc so prefer ld for it
RUN set -e; xx-apk add musl-dev gcc libseccomp-dev libseccomp-static; \
  [ "$(xx-info arch)" != "ppc64le" ] || XX_CC_PREFER_LINKER=ld xx-clang --setup-target-triple
RUN --mount=target=/root/.cache,type=cache <<EOT
  set -ex
  CGO_ENABLED=1 xx-go build -mod=vendor -ldflags '-extldflags -static' -tags 'apparmor seccomp netgo cgo static_build osusergo' -o /usr/bin/runc ./
  xx-verify --static /usr/bin/runc
  if [ "$(xx-info os)" = "linux" ]; then /usr/bin/runc --version; fi
EOT

FROM gobuild-base AS buildkit-base
WORKDIR /src
ENV GOFLAGS=-mod=vendor

# buildkit-version builds stage with version/revision info
FROM buildkit-base AS buildkit-version
# TODO: PKG should be inferred from go modules
RUN --mount=target=. <<'EOT'
  git rev-parse HEAD 2>/dev/null || {
    echo >&2 "Failed to get git revision, make sure --build-arg BUILDKIT_CONTEXT_KEEP_GIT_DIR=1 is set when building from Git directly"
    exit 1
  }
  set -ex
  export PKG=github.com/moby/buildkit VERSION=$(git describe --match 'v[0-9]*' --dirty='.m' --always --tags) REVISION=$(git rev-parse HEAD)$(if ! git diff --no-ext-diff --quiet --exit-code; then echo .m; fi);
  echo "-X ${PKG}/version.Version=${VERSION} -X ${PKG}/version.Revision=${REVISION} -X ${PKG}/version.Package=${PKG}" > /tmp/.ldflags;
  echo -n "${VERSION}" > /tmp/.version;
EOT

# buildctl builds test cli binary
FROM buildkit-base AS buildctl
ENV CGO_ENABLED=0
ARG TARGETPLATFORM
ARG GOBUILDFLAGS
RUN --mount=target=. --mount=target=/root/.cache,type=cache \
  --mount=target=/go/pkg/mod,type=cache \
  --mount=source=/tmp/.ldflags,target=/tmp/.ldflags,from=buildkit-version <<EOT
  set -ex
  xx-go build ${GOBUILDFLAGS} -ldflags "$(cat /tmp/.ldflags)" -o /usr/bin/buildctl ./cmd/buildctl
  xx-verify --static /usr/bin/buildctl
  if [ "$(xx-info os)" = "linux" ]; then /usr/bin/buildctl --version; fi
EOT

# buildkitd builds daemon binary
FROM buildkit-base AS buildkitd
# BUILDKITD_TAGS defines additional Go build tags for compiling buildkitd
ARG BUILDKITD_TAGS
ARG TARGETPLATFORM
ARG GOBUILDFLAGS
ARG VERIFYFLAGS="--static"
ARG CGO_ENABLED=0
ARG BUILDKIT_DEBUG
ARG GOGCFLAGS=${BUILDKIT_DEBUG:+"all=-N -l"}
RUN --mount=target=. --mount=target=/root/.cache,type=cache \
  --mount=target=/go/pkg/mod,type=cache \
  --mount=source=/tmp/.ldflags,target=/tmp/.ldflags,from=buildkit-version <<EOT
  set -ex
  xx-go build ${GOBUILDFLAGS} -gcflags="${GOGCFLAGS}" -ldflags "$(cat /tmp/.ldflags) -extldflags '-static'" -tags "osusergo netgo static_build seccomp ${BUILDKITD_TAGS}" -o /usr/bin/buildkitd ./cmd/buildkitd
  xx-verify ${VERIFYFLAGS} /usr/bin/buildkitd

  # buildkitd --version can be flaky when running through emulation related to
  # https://github.com/moby/buildkit/pull/4491. Retry a few times as a workaround.
  set +ex
  if [ "$(xx-info os)" = "linux" ]; then
    max_retries=5
    for attempt in $(seq "$max_retries"); do
      timeout 3 /usr/bin/buildkitd --version
      exitcode=$?
      if ! xx-info is-cross; then
        exit $exitcode
      elif [ $exitcode -eq 0 ]; then
        break
      elif [ $exitcode -eq 124 ] || [ $exitcode -eq 143 ]; then
        echo "WARN: buildkitd --version timed out ($attempt/$max_retries)"
        if [ "$attempt" -eq "$max_retries" ]; then
          exit $exitcode
        fi
      else
        echo "ERROR: buildkitd --version failed with exit code $exitcode"
      fi
      sleep 1
    done
  fi
EOT

# dnsname builds dnsname CNI plugin for testing
FROM gobuild-base AS dnsname
WORKDIR /go/src/github.com/containers/dnsname
ARG DNSNAME_VERSION
ADD --keep-git-dir=true "https://github.com/containers/dnsname.git#$DNSNAME_VERSION" .
ARG TARGETPLATFORM
RUN --mount=target=/root/.cache,type=cache \
    CGO_ENABLED=0 xx-go build -o /usr/bin/dnsname ./plugins/meta/dnsname && \
    xx-verify --static /usr/bin/dnsname

FROM --platform=$BUILDPLATFORM alpine:${ALPINE_VERSION} AS cni-plugins
RUN apk add --no-cache curl
COPY --from=xx / /
ARG CNI_VERSION
ARG TARGETOS
ARG TARGETARCH
ARG TARGETPLATFORM
WORKDIR /opt/cni/bin
RUN curl -Ls https://github.com/containernetworking/plugins/releases/download/${CNI_VERSION}/cni-plugins-${TARGETOS}-${TARGETARCH}-${CNI_VERSION}.tgz | tar xzv
RUN xx-verify --static bridge loopback host-local
COPY --link --from=dnsname /usr/bin/dnsname /opt/cni/bin/

FROM scratch AS cni-plugins-export
COPY --link --from=cni-plugins /opt/cni/bin/bridge /buildkit-cni-bridge
COPY --link --from=cni-plugins /opt/cni/bin/loopback /buildkit-cni-loopback
COPY --link --from=cni-plugins /opt/cni/bin/host-local /buildkit-cni-host-local
COPY --link --from=cni-plugins /opt/cni/bin/firewall /buildkit-cni-firewall

FROM scratch AS cni-plugins-export-squashed
COPY --from=cni-plugins-export / /

FROM scratch AS binaries-linux
COPY --link --from=runc /usr/bin/runc /buildkit-runc
# built from https://github.com/tonistiigi/binfmt/releases/tag/buildkit%2Fv7.1.0-30
COPY --link --from=tonistiigi/binfmt:buildkit-v7.1.0-30@sha256:45dd57b4ba2f24e2354f71f1e4e51f073cb7a28fd848ce6f5f2a7701142a6bf0 / /
COPY --link --from=cni-plugins-export-squashed / /
COPY --link --from=buildctl /usr/bin/buildctl /
COPY --link --from=buildkitd /usr/bin/buildkitd /

FROM scratch AS binaries-darwin
COPY --link --from=buildctl /usr/bin/buildctl /

FROM scratch AS binaries-windows
COPY --link --from=buildctl /usr/bin/buildctl /buildctl.exe
COPY --link --from=buildkitd /usr/bin/buildkitd /buildkitd.exe

FROM scratch AS binaries-freebsd
COPY --link --from=buildkitd /usr/bin/buildkitd /
COPY --link --from=buildctl /usr/bin/buildctl /

FROM binaries-$TARGETOS AS binaries
# BUILDKIT_SBOM_SCAN_STAGE enables SBOM scanning for this stage
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

FROM alpine:${ALPINE_VERSION} AS buildkit-export
RUN apk add --no-cache fuse3 git openssh pigz xz iptables ip6tables \
  && ln -s fusermount3 /usr/bin/fusermount
COPY --link examples/buildctl-daemonless/buildctl-daemonless.sh /usr/bin/
VOLUME /var/lib/buildkit

FROM gobuild-base AS containerd-build
WORKDIR /go/src/github.com/containerd/containerd
ARG TARGETPLATFORM
ENV CGO_ENABLED=1 BUILDTAGS=no_btrfs GO111MODULE=off
RUN xx-apk add musl-dev gcc && xx-go --wrap
COPY --chmod=755 <<-EOT /build.sh
#!/bin/sh
set -ex
mkdir /out
if [ "$(xx-info os)" = "linux" ]; then
  make bin/containerd
  make bin/containerd-shim-runc-v2
  mv bin/containerd bin/containerd-shim* /out
else
  CGO_ENABLED=0 make STATIC=1 binaries
  if [ "$(xx-info os)" = "windows" ]; then
    mv bin/containerd.exe /out
  else
    mv bin/containerd /out
  fi
  # No shim binary is built for FreeBSD, since containerd v2.0.
  if ls bin/containerd-shim* >/dev/null 2>&1; then
    mv bin/containerd-shim* /out
  fi
fi
EOT

FROM containerd-build AS containerd
WORKDIR /go/src/github.com/containerd/containerd
ARG CONTAINERD_VERSION
ADD --keep-git-dir=true "https://github.com/containerd/containerd.git#$CONTAINERD_VERSION" .
RUN /build.sh

# containerd-alt-17 builds containerd v1.7 for integration tests
FROM containerd-build AS containerd-alt-17
WORKDIR /go/src/github.com/containerd/containerd
ARG CONTAINERD_ALT_VERSION_17
ADD --keep-git-dir=true "https://github.com/containerd/containerd.git#$CONTAINERD_ALT_VERSION_17" .
RUN /build.sh

# containerd-alt-16 builds containerd v1.6 for integration tests
FROM containerd-build AS containerd-alt-16
WORKDIR /go/src/github.com/containerd/containerd
ARG CONTAINERD_ALT_VERSION_16
ADD --keep-git-dir=true "https://github.com/containerd/containerd.git#$CONTAINERD_ALT_VERSION_16" .
RUN /build.sh

FROM gobuild-base AS registry
WORKDIR /go/src/github.com/docker/distribution
ARG REGISTRY_VERSION
ADD --keep-git-dir=true "https://github.com/distribution/distribution.git#$REGISTRY_VERSION" .
ARG TARGETPLATFORM
RUN --mount=target=/root/.cache,type=cache <<EOT
  set -ex
  mkdir /out
  export GOPATH="$(pwd)/Godeps/_workspace:$GOPATH"
  GO111MODULE=off CGO_ENABLED=0 xx-go build -o /out/registry ./cmd/registry
  xx-verify --static /out/registry
  if [ "$(xx-info os)" = "windows" ]; then
    mv /out/registry /out/registry.exe
  fi
EOT

FROM gobuild-base AS rootlesskit
WORKDIR /go/src/github.com/rootless-containers/rootlesskit
ARG ROOTLESSKIT_VERSION
ADD --keep-git-dir=true "https://github.com/rootless-containers/rootlesskit.git#$ROOTLESSKIT_VERSION" .
ARG TARGETPLATFORM
RUN  --mount=target=/root/.cache,type=cache \
  CGO_ENABLED=0 xx-go build -o /rootlesskit ./cmd/rootlesskit && \
  xx-verify --static /rootlesskit

FROM gobuild-base AS stargz-snapshotter
WORKDIR /go/src/github.com/containerd/stargz-snapshotter
ARG STARGZ_SNAPSHOTTER_VERSION
ADD --keep-git-dir=true "https://github.com/containerd/stargz-snapshotter.git#$STARGZ_SNAPSHOTTER_VERSION" .
ARG TARGETPLATFORM
RUN --mount=target=/root/.cache,type=cache \
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
ARG TARGETPLATFORM
RUN --mount=target=/root/.cache,type=cache <<EOT
  set -ex
  xx-go install "gotest.tools/gotestsum@${GOTESTSUM_VERSION}"
  xx-go install "github.com/wadey/gocovmerge@latest"
  mkdir /out
  if ! xx-info is-cross; then
    /go/bin/gotestsum --version
    mv /go/bin/gotestsum /out
    mv /go/bin/gocovmerge /out
  else
    mv /go/bin/*/gotestsum* /out
    mv /go/bin/*/gocovmerge* /out
  fi
EOT
COPY --chmod=755 <<"EOF" /out/gotestsumandcover
#!/bin/sh
set -x
if [ -z "$GO_TEST_COVERPROFILE" ]; then
  exec gotestsum "$@"
fi
coverdir="$(dirname "$GO_TEST_COVERPROFILE")"
mkdir -p "$coverdir/helpers"
gotestsum "$@" "-coverprofile=$GO_TEST_COVERPROFILE"
ecode=$?
go tool covdata textfmt -i=$coverdir/helpers -o=$coverdir/helpers-report.txt
gocovmerge "$coverdir/helpers-report.txt" "$GO_TEST_COVERPROFILE" > "$coverdir/merged-report.txt"
mv "$coverdir/merged-report.txt" "$GO_TEST_COVERPROFILE"
rm "$coverdir/helpers-report.txt"
for f in "$coverdir/helpers"/*; do
  rm "$f"
done
rmdir "$coverdir/helpers"
exit $ecode
EOF

# dlv builds delve for debug variant images
FROM gobuild-base AS dlv
ARG DELVE_VERSION
ARG TARGETPLATFORM
RUN --mount=target=/root/.cache,type=cache\
    --mount=target=/go/pkg/mod,type=cache <<EOT
  set -ex
  mkdir /out
  if [ "$(xx-info os)" = "freebsd" ]; then
    echo "WARN: dlv requires cgo enabled on FreeBSD, skipping: https://github.com/moby/buildkit/pull/5497#issuecomment-2462031339"
    exit 0
  fi
  xx-go install "github.com/go-delve/delve/cmd/dlv@${DELVE_VERSION}"
  if ! xx-info is-cross; then
    /go/bin/dlv version
    mv /go/bin/dlv /out
  else
    mv /go/bin/*/dlv* /out
  fi
EOT

FROM buildkit-export AS buildkit-linux
COPY --link --from=binaries / /usr/bin/
ENV BUILDKIT_SETUP_CGROUPV2_ROOT=1
ENTRYPOINT ["buildkitd"]

FROM buildkit-linux AS buildkit-linux-debug
COPY --link --from=dlv /usr/bin/dlv /usr/bin/dlv
COPY --link --chmod=755 <<EOF /docker-entrypoint.sh
#!/bin/sh
exec dlv exec /usr/bin/buildkitd \\
  --api-version=2 \\
  -l 0.0.0.0:\${DELVE_PORT:-5000} \\
  --headless=true \\
  --accept-multiclient \\
  --continue \\
  -- "\$@"
EOF
ENV DELVE_PORT=5000
ENTRYPOINT ["/docker-entrypoint.sh"]

FROM binaries AS buildkit-darwin

FROM binaries AS buildkit-freebsd
ENTRYPOINT ["/buildkitd"]

FROM binaries AS buildkit-windows

FROM scratch AS binaries-for-test
COPY --link --from=gotestsum /out /
COPY --link --from=dlv /out /
COPY --link --from=registry /out /
COPY --link --from=containerd /out /
COPY --link --from=binaries / /

FROM buildkit-base AS integration-tests-base
ENV BUILDKIT_INTEGRATION_ROOTLESS_IDPAIR="1000:1000"
RUN apk add --no-cache shadow shadow-uidmap sudo vim iptables ip6tables dnsmasq fuse curl git-daemon openssh-client slirp4netns iproute2 \
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
RUN apk add --no-cache nodejs npm \
  && npm install -g azurite@${AZURITE_VERSION}
# The entrypoint script is needed for enabling nested cgroup v2 (https://github.com/moby/buildkit/issues/3265#issuecomment-1309631736)
RUN curl -Ls https://raw.githubusercontent.com/moby/moby/v25.0.1/hack/dind > /docker-entrypoint.sh \
  && chmod 0755 /docker-entrypoint.sh
ENTRYPOINT ["/docker-entrypoint.sh"]
# musl is needed to directly use the registry binary that is built on alpine
ENV BUILDKIT_INTEGRATION_CONTAINERD_EXTRA="containerd-1.7=/opt/containerd-alt-17/bin,containerd-1.6=/opt/containerd-alt-16/bin"
ENV BUILDKIT_INTEGRATION_SNAPSHOTTER=stargz
ENV BUILDKIT_SETUP_CGROUPV2_ROOT=1
ENV CGO_ENABLED=0
ENV GOTESTSUM_FORMAT=standard-verbose
COPY --link --from=gotestsum /out /usr/bin/
COPY --link --from=minio /opt/bin/minio /usr/bin/
COPY --link --from=minio-mc /usr/bin/mc /usr/bin/
COPY --link --from=nydus /out/nydus-static/* /usr/bin/
COPY --link --from=stargz-snapshotter /out/* /usr/bin/
COPY --link --from=rootlesskit /rootlesskit /usr/bin/
COPY --link --from=containerd-alt-17 /out/containerd* /opt/containerd-alt-17/bin/
COPY --link --from=containerd-alt-16 /out/containerd* /opt/containerd-alt-16/bin/
COPY --link --from=registry /out /usr/bin/
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

# rootless builds a rootless variant of buildkitd image
FROM alpine:${ALPINE_VERSION} AS rootless
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
ENV HOME=/home/user
ENV USER=user
ENV XDG_RUNTIME_DIR=/run/user/1000
ENV TMPDIR=/home/user/.local/tmp
ENV BUILDKIT_HOST=unix:///run/user/1000/buildkit/buildkitd.sock
VOLUME /home/user/.local/share/buildkit
ENTRYPOINT ["rootlesskit", "buildkitd"]

# buildkit builds the buildkit container image
FROM buildkit-$TARGETOS${BUILDKIT_DEBUG:+-debug} AS buildkit
