# syntax=docker/dockerfile-upstream:master

ARG GO_VERSION=1.20

FROM golang:${GO_VERSION}-alpine
ENV GOFLAGS="-buildvcs=false"
RUN apk add --no-cache gcc musl-dev yamllint
RUN wget -O- -nv https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s v1.52.2
WORKDIR /go/src/github.com/moby/buildkit
RUN --mount=target=/go/src/github.com/moby/buildkit --mount=target=/root/.cache,type=cache \
  GOARCH=amd64 golangci-lint run && \
  GOARCH=arm64 golangci-lint run
RUN --mount=target=/go/src/github.com/moby/buildkit --mount=target=/root/.cache,type=cache \
  yamllint -c .yamllint.yml --strict .
