# syntax=docker/dockerfile:1.3

ARG GOSEC_VERSION=v2.9.1

FROM securego/gosec:${GOSEC_VERSION} AS validate
WORKDIR /go/src/github.com/moby/buildkit
RUN --mount=target=. ./hack/validate-gosec check
