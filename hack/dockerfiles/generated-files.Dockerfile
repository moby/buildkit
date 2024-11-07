# syntax=docker/dockerfile-upstream:master

ARG GO_VERSION=1.23
ARG DEBIAN_VERSION=bookworm
ARG PROTOC_VERSION=3.11.4
ARG PROTOC_GOOGLEAPIS_VERSION=2af421884dd468d565137215c946ebe4e245ae26

# protoc is dynamically linked to glibc so can't use alpine base

# base defines common stage with Go tools
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
  mkdir -p /opt/googleapis
  mv /opt/googleapis-${PROTOC_GOOGLEAPIS_VERSION} /opt/googleapis/include
EOT

FROM base AS gobuild-base
WORKDIR /app

FROM gobuild-base AS vtprotobuf
RUN --mount=type=bind,source=go.mod,target=/app/go.mod \
    --mount=type=bind,source=go.sum,target=/app/go.sum \
    --mount=type=cache,target=/root/.cache \
    --mount=type=cache,target=/go/pkg/mod <<EOT
  set -e
  mkdir -p /opt/vtprotobuf
  go mod download github.com/planetscale/vtprotobuf
  cp -r $(go list -m -f='{{.Dir}}' github.com/planetscale/vtprotobuf)/include /opt/vtprotobuf
EOT

FROM gobuild-base AS vendored
RUN --mount=type=bind,source=vendor,target=/app <<EOT
  set -e
  mkdir -p /opt/vendored/include
  find . -name '*.proto' | tar -cf - --files-from - | tar -C /opt/vendored/include -xf -
EOT

FROM gobuild-base AS tools
RUN --mount=type=bind,source=go.mod,target=/app/go.mod \
    --mount=type=bind,source=go.sum,target=/app/go.sum \
    --mount=type=cache,target=/root/.cache \
    --mount=type=cache,target=/go/pkg/mod \
  go install \
    github.com/planetscale/vtprotobuf/cmd/protoc-gen-go-vtproto \
    google.golang.org/grpc/cmd/protoc-gen-go-grpc \
    google.golang.org/protobuf/cmd/protoc-gen-go
COPY --link --from=protoc /opt/protoc /usr/local
COPY --link --from=googleapis /opt/googleapis /usr/local
COPY --link --from=vtprotobuf /opt/vtprotobuf /usr/local
COPY --link --from=vendored /opt/vendored /usr/local

FROM tools AS generated
RUN --mount=type=bind,target=github.com/moby/buildkit <<EOT
  set -ex
  mkdir /out
  find github.com/moby/buildkit -name '*.proto' -o -name vendor -prune -false | xargs \
    protoc --go_out=/out --go-grpc_out=require_unimplemented_servers=false:/out \
           --go-vtproto_out=features=marshal+unmarshal+size+equal+pool+clone:/out
EOT

FROM scratch AS update
COPY --from=generated /out/github.com/moby/buildkit /

FROM gobuild-base AS validate
RUN --mount=type=bind,target=.,rw \
    --mount=type=bind,from=update,target=/generated-files <<EOT
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
