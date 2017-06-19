ARG RUNC_VERSION=v1.0.0-rc3
ARG CONTAINERD_VERSION=7e3b7dead60d96e9a7b13b8813d1712c7761e327

FROM golang:1.8-alpine AS gobuild-base
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
	&& git checkout -q "$RUNC_VERSION" \
	&& make bin/containerd

FROM gobuild-base
COPY --from=runc /usr/bin/runc /usr/bin/runc 
COPY --from=containerd /go/src/github.com/containerd/containerd/bin/containerd /usr/bin/containerd 
WORKDIR /go/src/github.com/tonistiigi/buildkit_poc
COPY . .
RUN go build -o /usr/bin/buildd-standalone -tags standalone ./cmd/buildd
RUN go build -o /usr/bin/buildd-containerd -tags containerd ./cmd/buildd
