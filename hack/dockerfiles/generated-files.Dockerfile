# syntax=docker/dockerfile-upstream:master

ARG GO_VERSION="1.21"
ARG PROTOC_VERSION="3.11.4"

# protoc is dynamically linked to glibc so can't use alpine base
FROM golang:${GO_VERSION}-bookworm AS base
RUN apt-get update && apt-get --no-install-recommends install -y git unzip
ARG PROTOC_VERSION
ARG TARGETOS
ARG TARGETARCH
RUN <<EOT
  set -e
  arch=$(echo $TARGETARCH | sed -e s/amd64/x86_64/ -e s/arm64/aarch_64/)
  wget -q https://github.com/protocolbuffers/protobuf/releases/download/v${PROTOC_VERSION}/protoc-${PROTOC_VERSION}-${TARGETOS}-${arch}.zip
  unzip protoc-${PROTOC_VERSION}-${TARGETOS}-${arch}.zip -d /usr/local

  # Download status.proto. grpc repos' one seems copied from
  # https://github.com/googleapis/googleapis/blob/master/google/rpc/status.proto,
  # but we use grpc's since the repos has tags/releases.
  mkdir -p /usr/local/include/google/rpc
  curl \
	  -L https://raw.githubusercontent.com/grpc/grpc/v1.45.2/src/proto/grpc/status/status.proto \
	  -o /usr/local/include/google/rpc/status.proto
EOT
WORKDIR /go/src/github.com/moby/buildkit

FROM base AS tools
RUN --mount=type=bind,target=.,rw \
    --mount=type=cache,target=/root/.cache \
    --mount=type=cache,target=/go/pkg/mod \
    go install \
      github.com/golang/protobuf/protoc-gen-go \
      google.golang.org/grpc/cmd/protoc-gen-go-grpc

FROM tools AS generated
RUN --mount=type=bind,target=.,rw <<EOT
  set -ex
  go generate -mod=vendor -v ./...
  mkdir /out
  git ls-files -m --others -- ':!vendor' '**/*.pb.go' | tar -cf - --files-from - | tar -C /out -xf -
EOT

FROM scratch AS update
COPY --from=generated /out /

FROM base AS validate
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
