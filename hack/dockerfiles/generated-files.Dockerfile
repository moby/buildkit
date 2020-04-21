# syntax=docker/dockerfile:1.1-experimental

# protoc is dynamically linked to glibc to can't use golang:1.10-alpine
FROM golang:1.13-buster AS gobuild-base
ARG PROTOC_VERSION=3.1.0
ARG GOGO_VERSION=master
RUN apt-get update && apt-get --no-install-recommends install -y \
	git \
	unzip \
	&& true
RUN wget -q https://github.com/google/protobuf/releases/download/v${PROTOC_VERSION}/protoc-${PROTOC_VERSION}-linux-x86_64.zip && unzip protoc-${PROTOC_VERSION}-linux-x86_64.zip -d /usr/local

RUN go get -d github.com/gogo/protobuf/protoc-gen-gogofaster \
	&& cd /go/src/github.com/gogo/protobuf \
	&& git checkout -q $GOGO_VERSION \
	&& go install ./protoc-gen-gogo ./protoc-gen-gogofaster ./protoc-gen-gogoslick

ARG PROTOBUF_VERSION=v1.3.3
RUN go get -d github.com/golang/protobuf/protoc-gen-go \
	&& cd /go/src/github.com/golang/protobuf \
	&& git checkout -q $PROTOBUF_VERSION \
	&& go install ./protoc-gen-go

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
	go generate ./... && \
	git ls-files -m --others -- **/*.pb.go | tar -cf - --files-from - | tar -C /generated-files -xf -

FROM scratch AS update
COPY --from=generated /generated-files /generated-files

FROM gobuild-base AS validate
RUN --mount=target=/tmp/src \
	cp -r /tmp/src/. . && \
	go generate ./... && git diff && ./hack/validate-generated-files check
