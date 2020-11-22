# syntax=docker/dockerfile:1.1-experimental

FROM golang:1.13-alpine
RUN apk add --no-cache gcc musl-dev yamllint
RUN wget -O- -nv https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s v1.27.0
WORKDIR /go/src/github.com/moby/buildkit
RUN --mount=target=/go/src/github.com/moby/buildkit --mount=target=/root/.cache,type=cache \
  golangci-lint run
RUN --mount=target=/go/src/github.com/moby/buildkit --mount=target=/root/.cache,type=cache \
  yamllint -c .yamllint.yml --strict .
