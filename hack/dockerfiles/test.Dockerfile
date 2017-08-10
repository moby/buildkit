ARG RUNC_VERSION=e775f0fba3ea329b8b766451c892c41a3d49594d
ARG CONTAINERD_VERSION=d1e11f17ec7b325f89608dd46c128300b8727d50

FROM golang:1.8-alpine@sha256:2287e0e274c1d2e9076c1f81d04f1a63c86b73c73603b09caada5da307a8f86d AS gobuild-base
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
	&& make bin/containerd-shim

FROM gobuild-base AS unit-tests
COPY --from=runc /usr/bin/runc /usr/bin/runc
COPY --from=containerd /go/src/github.com/containerd/containerd/bin/containerd* /usr/bin/
WORKDIR /go/src/github.com/moby/buildkit
COPY . .

FROM unit-tests AS buildctl
ENV CGO_ENABLED=0
ARG GOOS=linux
RUN go build -ldflags '-d' -o /usr/bin/buildctl ./cmd/buildctl

FROM unit-tests AS buildd-standalone
ENV CGO_ENABLED=0
RUN go build -ldflags '-d'  -o /usr/bin/buildd-standalone -tags standalone ./cmd/buildd

FROM unit-tests AS buildd-containerd
ENV CGO_ENABLED=0
RUN go build -ldflags '-d'  -o /usr/bin/buildd-containerd -tags containerd ./cmd/buildd

FROM unit-tests AS integration-tests
COPY --from=buildd-containerd /usr/bin/buildd-containerd /usr/bin
COPY --from=buildd-standalone /usr/bin/buildd-standalone /usr/bin

FROM gobuild-base AS cross-windows
ENV GOOS=windows
WORKDIR /go/src/github.com/moby/buildkit
COPY . .

FROM cross-windows AS buildctl.exe
RUN go build -o /buildctl.exe ./cmd/buildctl

FROM cross-windows AS buildd.exe
RUN go build -o /buildd.exe ./cmd/buildd
