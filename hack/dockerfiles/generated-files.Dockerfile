# syntax=docker/dockerfile-upstream:master

ARG GO_VERSION=1.22
ARG DEBIAN_VERSION=bookworm
ARG PROTOC_VERSION=3.11.4
ARG PROTOC_GOOGLEAPIS_VERSION=2af421884dd468d565137215c946ebe4e245ae26

# protoc is dynamically linked to glibc so can't use alpine base
FROM golang:${GO_VERSION}-${DEBIAN_VERSION} AS base
RUN apt-get update && apt-get --no-install-recommends install -y git unzip

FROM base AS protoc
ARG PROTOC_VERSION
ARG TARGETOS
ARG TARGETARCH
RUN <<EOT
  set -e
  arch=$(echo $TARGETARCH | sed -e s/amd64/x86_64/ -e s/arm64/aarch_64/)
  wget -q https://github.com/protocolbuffers/protobuf/releases/download/v${PROTOC_VERSION}/protoc-${PROTOC_VERSION}-${TARGETOS}-${arch}.zip
  unzip protoc-${PROTOC_VERSION}-${TARGETOS}-${arch}.zip -d /opt/protoc
EOT

FROM base AS googleapis
ARG PROTOC_GOOGLEAPIS_VERSION
RUN <<EOT
  set -e
  wget -q https://github.com/googleapis/googleapis/archive/${PROTOC_GOOGLEAPIS_VERSION}.zip -O googleapis.zip
  unzip googleapis.zip '*.proto' -d /opt
  mv /opt/googleapis-${PROTOC_GOOGLEAPIS_VERSION} /opt/googleapis
EOT

FROM base AS gobuild-base
WORKDIR /go/src/github.com/moby/buildkit
COPY --link --from=protoc /opt/protoc /usr/local
COPY --link --from=googleapis /opt/googleapis /usr/local/include

FROM gobuild-base AS tools
RUN --mount=type=bind,target=.,rw \
    --mount=type=cache,target=/root/.cache \
    --mount=type=cache,target=/go/pkg/mod \
    go install \
      google.golang.org/grpc/cmd/protoc-gen-go-grpc \
      google.golang.org/protobuf/cmd/protoc-gen-go

FROM tools AS generated
RUN --mount=type=bind,target=.,rw <<EOT
  set -ex
  go generate -mod=vendor -v ./...
  mkdir /out
  git ls-files -m --others -- ':!vendor' '**/*.pb.go' | tar -cf - --files-from - | tar -C /out -xf -
EOT

FROM scratch AS update
COPY --from=generated /out /

FROM gobuild-base AS validate
RUN --mount=type=bind,target=.,rw \
    --mount=type=bind,from=generated,source=/out,target=/generated-files <<EOT
  set -e
  git add -A
  if [ "$(ls -A /generated-files)" ]; then
    cp -rf /generated-files/* .
  fi
  diff=$(git status --porcelain -- ':!vendor' '**/*.pb.go')
  if [ -n "$diff" ]; then
    echo >&2 'ERROR: The result of "go generate" differs. Please update with "make generated-files"'
    echo "$diff"
    exit 1
  fi
EOT
