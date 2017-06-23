FROM golang:1.8
RUN apt-get update
ARG CONTAINERD_REPO=git://github.com/tonistiigi/containerd.git
ARG CONTAINERD_VERSION=39d55cc498982f9f3f3be313338467011211efb9
WORKDIR /go/src/github.com/containerd
RUN git clone $CONTAINERD_REPO containerd && cd containerd
WORKDIR /go/src/github.com/moby/buildkit
COPY . .


# git://github.com/tonistiigi/containerd.git
# 39d55cc498982f9f3f3be313338467011211efb9[[m

# cp -a containerd/containerd/vendor/. /go/src/
#

# git clone git://github.com/tonistiigi/containerd.git
# cd containerd
# git checkout 39d55cc498982f9f3f3be313338467011211efb9
# cp -a ./vendor/. /go/src/
# rm -rf ./vendor/*
#  go build --buildmode=plugin -o /buildkit-linux-amd64.so ./cmd/ctd-plugin/
# #
# rm -rf vendor/github.com/containerd/containerd
# rm -rf vendor/github.com/opencontainers/image-spec
# rm -rf vendor/github.com/opencontainers/runtime-spec
# rm -rf vendor/github.com/opencontainers/go-digest