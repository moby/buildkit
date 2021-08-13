# syntax=docker/dockerfile:1.3

# protoc is dynamically linked to glibc to can't use golang:1.10-alpine
FROM golang:1.16-buster AS gobuild-base

RUN apt-get update && apt-get --no-install-recommends install -y \
	unzip \
	&& true

# https://github.com/golang/protobuf/blob/v1.3.5/.travis.yml#L15
ARG PROTOC_VERSION=3.17.3
ARG TARGETOS TARGETARCH
RUN set -e; \
	arch=$(echo $TARGETARCH | sed -e s/amd64/x86_64/ -e s/arm64/aarch_64/); \
	wget -q https://github.com/protocolbuffers/protobuf/releases/download/v${PROTOC_VERSION}/protoc-${PROTOC_VERSION}-${TARGETOS}-${arch}.zip && unzip protoc-${PROTOC_VERSION}-${TARGETOS}-${arch}.zip -d /usr/local

ARG GOGO_VERSION=v1.3.2
RUN --mount=target=/root/.cache,type=cache GO111MODULE=on go install \
	github.com/gogo/protobuf/protoc-gen-gogo@${GOGO_VERSION} \
	github.com/gogo/protobuf/protoc-gen-gogofaster@${GOGO_VERSION} \
	github.com/gogo/protobuf/protoc-gen-gogoslick@${GOGO_VERSION}

ARG PROTOBUF_VERSION=v1.5.2
RUN --mount=target=/root/.cache,type=cache GO111MODULE=on go install \
	github.com/golang/protobuf/protoc-gen-go@${PROTOBUF_VERSION}

WORKDIR /go/src/github.com/moby/buildkit

# Generate into a subdirectory because if it is in the root then the
# extraction with `docker export` ends up putting `.dockerenv`, `dev`,
# `sys` and `proc` into the source directory. With this we can use
# `tar --strip-components=1 generated-files` on the output of `docker
# export`.
FROM gobuild-base AS generated
RUN mkdir /generated-files
RUN --mount=target=/tmp/src \
	cp -r /tmp/src/. . && \
	git add -A && \
	go generate -mod=vendor -v ./... && \
	git ls-files -m --others -- **/*.pb.go | tar -cf - --files-from - | tar -C /generated-files -xf -

FROM scratch AS update
COPY --from=generated /generated-files /generated-files

FROM gobuild-base AS validate
RUN --mount=target=/tmp/src \
	cp -r /tmp/src/. . && \
	go generate -mod=vendor -v ./... && git diff && ./hack/validate-generated-files check
