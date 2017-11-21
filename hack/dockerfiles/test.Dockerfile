ARG RUNC_VERSION=74a17296470088de3805e138d3d87c62e613dfc4
ARG CONTAINERD_VERSION=v1.0.0
# available targets: buildd (standalone+containerd), buildd-standalone, buildd-containerd
ARG BUILDKIT_TARGET=buildd
ARG REGISTRY_VERSION=2.6

FROM golang:1.9-alpine AS gobuild-base
RUN apk add --no-cache g++ linux-headers
RUN apk add --no-cache git make

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

FROM gobuild-base AS buildkit-base
WORKDIR /go/src/github.com/moby/buildkit
COPY . .

FROM buildkit-base AS unit-tests
COPY --from=runc /usr/bin/runc /usr/bin/runc
COPY --from=containerd /go/src/github.com/containerd/containerd/bin/containerd* /usr/bin/

FROM buildkit-base AS buildctl
ENV CGO_ENABLED=0
ARG GOOS=linux
RUN go build -ldflags '-d' -o /usr/bin/buildctl ./cmd/buildctl

FROM buildkit-base AS buildd-standalone
ENV CGO_ENABLED=0
RUN go build -ldflags '-d'  -o /usr/bin/buildd-standalone -tags standalone ./cmd/buildd

FROM buildkit-base AS buildd-containerd
ENV CGO_ENABLED=0
RUN go build -ldflags '-d'  -o /usr/bin/buildd-containerd -tags containerd ./cmd/buildd

FROM registry:$REGISTRY_VERSION AS registry

FROM buildkit-base AS buildd
ENV CGO_ENABLED=0
RUN go build -ldflags '-d'  -o /usr/bin/buildd -tags "standalone containerd" ./cmd/buildd

FROM unit-tests AS integration-tests
COPY --from=buildctl /usr/bin/buildctl /usr/bin/
COPY --from=buildd-containerd /usr/bin/buildd-containerd /usr/bin
COPY --from=buildd-standalone /usr/bin/buildd-standalone /usr/bin
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

# Copy together all binaries needed for standalone mode
FROM buildkit-export AS buildkit-buildd-standalone
COPY --from=buildd-standalone /usr/bin/buildd-standalone /usr/bin/
COPY --from=buildctl /usr/bin/buildctl /usr/bin/
ENTRYPOINT ["buildd-standalone"]

# Copy together all binaries for containerd mode
FROM buildkit-export AS buildkit-buildd-containerd
COPY --from=runc /usr/bin/runc /usr/bin/
COPY --from=buildd-containerd /usr/bin/buildd-containerd /usr/bin/
COPY --from=buildctl /usr/bin/buildctl /usr/bin/
ENTRYPOINT ["buildd-containerd"]

# Copy together all binaries for standalone+containerd mode
FROM buildkit-export AS buildkit-buildd
COPY --from=runc /usr/bin/runc /usr/bin/
COPY --from=buildd /usr/bin/buildd /usr/bin/
COPY --from=buildctl /usr/bin/buildctl /usr/bin/

FROM alpine AS containerd-runtime
COPY --from=runc /usr/bin/runc /usr/bin/
COPY --from=containerd /go/src/github.com/containerd/containerd/bin/containerd* /usr/bin/
COPY --from=containerd /go/src/github.com/containerd/containerd/bin/ctr /usr/bin/
VOLUME /var/lib/containerd
VOLUME /run/containerd
ENTRYPOINT ["containerd"]

FROM buildkit-${BUILDKIT_TARGET}


