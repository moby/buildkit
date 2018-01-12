ARG RUNC_VERSION=7f24b40cc5423969b4554ef04ba0b00e2b4ba010
ARG CONTAINERD_VERSION=v1.0.1-rc.0
# available targets: buildkitd, buildkitd.oci_only, buildkitd.containerd_only
ARG BUILDKIT_TARGET=buildkitd
ARG REGISTRY_VERSION=2.6

# The `buildkitd` stage and the `buildctl` stage are placed here
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

FROM buildkit-base AS buildkitd
ENV CGO_ENABLED=0
RUN go build -ldflags '-d'  -o /usr/bin/buildkitd ./cmd/buildkitd

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


FROM buildkit-base AS buildkitd.oci_only
ENV CGO_ENABLED=0
RUN go build -ldflags '-d'  -o /usr/bin/buildkitd.oci_only -tags no_containerd_worker ./cmd/buildkitd

FROM buildkit-base AS buildkitd.containerd_only
ENV CGO_ENABLED=0
RUN go build -ldflags '-d'  -o /usr/bin/buildkitd.containerd_only -tags no_oci_worker ./cmd/buildkitd

FROM registry:$REGISTRY_VERSION AS registry

FROM unit-tests AS integration-tests
COPY --from=buildctl /usr/bin/buildctl /usr/bin/
COPY --from=buildkitd /usr/bin/buildkitd /usr/bin
COPY --from=registry /bin/registry /usr/bin

FROM gobuild-base AS cross-windows
ENV GOOS=windows
WORKDIR /go/src/github.com/moby/buildkit
COPY . .

FROM cross-windows AS buildctl.exe
RUN go build -o /buildctl.exe ./cmd/buildctl

FROM cross-windows AS buildkitd.exe
RUN go build -o /buildkitd.exe ./cmd/buildkitd

FROM alpine AS buildkit-export
RUN apk add --no-cache git
VOLUME /var/lib/buildkit

# Copy together all binaries for oci+containerd mode
FROM buildkit-export AS buildkit-buildkitd
COPY --from=runc /usr/bin/runc /usr/bin/
COPY --from=buildkitd /usr/bin/buildkitd /usr/bin/
COPY --from=buildctl /usr/bin/buildctl /usr/bin/
ENTRYPOINT ["buildkitd"]

# Copy together all binaries needed for oci worker mode
FROM buildkit-export AS buildkit-buildkitd.oci_only
COPY --from=buildkitd.oci_only /usr/bin/buildkitd.oci_only /usr/bin/
COPY --from=buildctl /usr/bin/buildctl /usr/bin/
ENTRYPOINT ["buildkitd.oci_only"]

# Copy together all binaries for containerd worker mode
FROM buildkit-export AS buildkit-buildkitd.containerd_only
COPY --from=runc /usr/bin/runc /usr/bin/
COPY --from=buildkitd.containerd_only /usr/bin/buildkitd.containerd_only /usr/bin/
COPY --from=buildctl /usr/bin/buildctl /usr/bin/
ENTRYPOINT ["buildkitd.containerd_only"]

FROM alpine AS containerd-runtime
COPY --from=runc /usr/bin/runc /usr/bin/
COPY --from=containerd /go/src/github.com/containerd/containerd/bin/containerd* /usr/bin/
COPY --from=containerd /go/src/github.com/containerd/containerd/bin/ctr /usr/bin/
VOLUME /var/lib/containerd
VOLUME /run/containerd
ENTRYPOINT ["containerd"]

FROM buildkit-${BUILDKIT_TARGET}


