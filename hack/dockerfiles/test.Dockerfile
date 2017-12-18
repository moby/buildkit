ARG RUNC_VERSION=74a17296470088de3805e138d3d87c62e613dfc4
ARG CONTAINERD_VERSION=v1.0.0
# available targets: buildd, buildd.oci_only, buildd.containerd_only
ARG BUILDKIT_TARGET=buildd
ARG REGISTRY_VERSION=2.6

# The `buildd` stage and the `buildctl` stage are placed here
# so that they can be built quickly with legacy DAG-unaware `docker build --target=...`

FROM golang:1.9-alpine AS gobuild-base
RUN apk add --no-cache g++ linux-headers
RUN apk add --no-cache git make

FROM gobuild-base AS buildkit-base
WORKDIR /go/src/github.com/moby/buildkit
COPY . .

FROM buildkit-base AS buildctl
ENV CGO_ENABLED=0
ARG GOOS=linux
RUN go build -ldflags '-d' -o /usr/bin/buildctl ./cmd/buildctl

FROM buildkit-base AS buildd
ENV CGO_ENABLED=0
RUN go build -ldflags '-d'  -o /usr/bin/buildd ./cmd/buildd

# test dependencies begin here
FROM gobuild-base AS runc
ARG RUNC_VERSION
RUN git clone https://github.com/opencontainers/runc.git "$GOPATH/src/github.com/opencontainers/runc" \
	&& cd "$GOPATH/src/github.com/opencontainers/runc" \
	&& git checkout -q "$RUNC_VERSION" \
	&& go build -o /usr/bin/runc ./

FROM gobuild-base AS containerd
RUN apk add --no-cache btrfs-progs-dev
ARG CONTAINERD_VERSION
RUN git clone https://github.com/containerd/containerd.git "$GOPATH/src/github.com/containerd/containerd" \
	&& cd "$GOPATH/src/github.com/containerd/containerd" \
	&& git checkout -q "$CONTAINERD_VERSION" \
	&& make bin/containerd \
	&& make bin/containerd-shim \
	&& make bin/ctr

FROM buildkit-base AS unit-tests
COPY --from=runc /usr/bin/runc /usr/bin/runc
COPY --from=containerd /go/src/github.com/containerd/containerd/bin/containerd* /usr/bin/


FROM buildkit-base AS buildd.oci_only
ENV CGO_ENABLED=0
RUN go build -ldflags '-d'  -o /usr/bin/buildd.oci_only -tags no_containerd_worker ./cmd/buildd

FROM buildkit-base AS buildd.containerd_only
ENV CGO_ENABLED=0
RUN go build -ldflags '-d'  -o /usr/bin/buildd.containerd_only -tags no_oci_worker ./cmd/buildd

FROM registry:$REGISTRY_VERSION AS registry

FROM unit-tests AS integration-tests
COPY --from=buildctl /usr/bin/buildctl /usr/bin/
COPY --from=buildd /usr/bin/buildd /usr/bin
COPY --from=registry /bin/registry /usr/bin

FROM gobuild-base AS cross-windows
ENV GOOS=windows
WORKDIR /go/src/github.com/moby/buildkit
COPY . .

FROM cross-windows AS buildctl.exe
RUN go build -o /buildctl.exe ./cmd/buildctl

FROM cross-windows AS buildd.exe
RUN go build -o /buildd.exe ./cmd/buildd

FROM alpine AS buildkit-export
RUN apk add --no-cache git
VOLUME /var/lib/buildkit

# Copy together all binaries for oci+containerd mode
FROM buildkit-export AS buildkit-buildd
COPY --from=runc /usr/bin/runc /usr/bin/
COPY --from=buildd /usr/bin/buildd /usr/bin/
COPY --from=buildctl /usr/bin/buildctl /usr/bin/
ENTRYPOINT ["buildd"]

# Copy together all binaries needed for oci worker mode
FROM buildkit-export AS buildkit-buildd.oci_only
COPY --from=buildd.oci_only /usr/bin/buildd.oci_only /usr/bin/
COPY --from=buildctl /usr/bin/buildctl /usr/bin/
ENTRYPOINT ["buildd.oci_only"]

# Copy together all binaries for containerd worker mode
FROM buildkit-export AS buildkit-buildd.containerd_only
COPY --from=runc /usr/bin/runc /usr/bin/
COPY --from=buildd.containerd_only /usr/bin/buildd.containerd_only /usr/bin/
COPY --from=buildctl /usr/bin/buildctl /usr/bin/
ENTRYPOINT ["buildd.containerd_only"]

FROM alpine AS containerd-runtime
COPY --from=runc /usr/bin/runc /usr/bin/
COPY --from=containerd /go/src/github.com/containerd/containerd/bin/containerd* /usr/bin/
COPY --from=containerd /go/src/github.com/containerd/containerd/bin/ctr /usr/bin/
VOLUME /var/lib/containerd
VOLUME /run/containerd
ENTRYPOINT ["containerd"]

FROM buildkit-${BUILDKIT_TARGET}


