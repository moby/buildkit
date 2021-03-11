ARG FUSEOVERLAYFS_COMMIT=master
ARG ROOTLESSKIT_COMMIT=v0.14.0-beta.0
ARG SHADOW_COMMIT=4.8.1

ARG GO_VERSION=1.16
ARG DEBIAN_VERSION=10
ARG ALPINE_VERSION=3.13

FROM golang:${GO_VERSION}-alpine AS containerd-fuse-overlayfs-test
COPY . /go/src/github.com/AkihiroSuda/containerd-fuse-overlayfs
WORKDIR  /go/src/github.com/AkihiroSuda/containerd-fuse-overlayfs
ENV CGO_ENABLED=0
ENV GO111MODULE=on
RUN mkdir /out && go test -c -o /out/containerd-fuse-overlayfs.test

# from https://github.com/containers/fuse-overlayfs/blob/53c17dab78b43de1cd121bf9260b20b76371bbaf/Dockerfile.static.ubuntu
FROM debian:${DEBIAN_VERSION} AS fuse-overlayfs
RUN apt-get update && \
    apt-get install --no-install-recommends -y \
        git ca-certificates libc6-dev gcc g++ make automake autoconf clang pkgconf libfuse3-dev
RUN git clone https://github.com/containers/fuse-overlayfs
WORKDIR fuse-overlayfs
ARG FUSEOVERLAYFS_COMMIT
RUN git pull && git checkout ${FUSEOVERLAYFS_COMMIT}
RUN  ./autogen.sh && \
     LIBS="-ldl" LDFLAGS="-static" ./configure && \
     make && mkdir /out && cp fuse-overlayfs /out

FROM golang:${GO_VERSION}-alpine AS rootlesskit
RUN apk add --no-cache git
RUN git clone https://github.com/rootless-containers/rootlesskit.git /go/src/github.com/rootless-containers/rootlesskit
WORKDIR /go/src/github.com/rootless-containers/rootlesskit
ARG ROOTLESSKIT_COMMIT
RUN git pull && git checkout ${ROOTLESSKIT_COMMIT}
ENV CGO_ENABLED=0
RUN mkdir /out && go build -o /out/rootlesskit github.com/rootless-containers/rootlesskit/cmd/rootlesskit 

FROM alpine:${ALPINE_VERSION} AS idmap
RUN apk add --no-cache autoconf automake build-base byacc gettext gettext-dev gcc git libcap-dev libtool libxslt
RUN git clone https://github.com/shadow-maint/shadow.git
WORKDIR shadow
ARG SHADOW_COMMIT
RUN git pull && git checkout ${SHADOW_COMMIT}
RUN ./autogen.sh --disable-nls --disable-man --without-audit --without-selinux --without-acl --without-attr --without-tcb --without-nscd && \
   make && mkdir -p /out && cp src/newuidmap src/newgidmap /out

FROM alpine:${ALPINE_VERSION}
COPY --from=containerd-fuse-overlayfs-test /out/containerd-fuse-overlayfs.test /usr/local/bin
COPY --from=rootlesskit /out/rootlesskit /usr/local/bin
COPY --from=fuse-overlayfs /out/fuse-overlayfs /usr/local/bin
COPY --from=idmap /out/newuidmap /usr/bin/newuidmap
COPY --from=idmap /out/newgidmap /usr/bin/newgidmap
RUN apk add --no-cache fuse3 libcap && \
    setcap CAP_SETUID=ep /usr/bin/newuidmap && \
    setcap CAP_SETGID=ep /usr/bin/newgidmap && \
    adduser -D -u 1000 testuser && \
    echo testuser:100000:65536 | tee /etc/subuid | tee /etc/subgid
USER testuser
# If /tmp is real overlayfs, some tests fail. Mount a volume to ensure /tmp to be a sane filesystem.
VOLUME /tmp
# requires --security-opt seccomp=unconfined --security-opt apparmor=unconfined --device /dev/fuse 
CMD ["rootlesskit", "containerd-fuse-overlayfs.test", "-test.root", "-test.v"]
