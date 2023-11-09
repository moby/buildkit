# syntax=docker/dockerfile-upstream:master

ARG GO_VERSION=1.21
ARG PROTOLINT_VERSION=0.45.0
ARG GOLANGCI_LINT_VERSION=1.55.0

FROM golang:${GO_VERSION}-alpine AS golang-base
FROM yoheimuta/protolint:${PROTOLINT_VERSION} AS protolint-base

FROM golang-base AS base
ENV GOFLAGS="-buildvcs=false"
ARG GOLANGCI_LINT_VERSION
RUN apk add --no-cache gcc musl-dev yamllint
RUN wget -O- -nv https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s v${GOLANGCI_LINT_VERSION}
COPY --link --from=protolint-base /usr/local/bin/protolint /usr/local/bin/protolint
WORKDIR /go/src/github.com/moby/buildkit

FROM base as golangci-lint
ARG BUILDTAGS
RUN --mount=target=/go/src/github.com/moby/buildkit --mount=target=/root/.cache,type=cache,sharing=locked \
  GOOS=linux GOARCH=amd64 golangci-lint run --build-tags "${BUILDTAGS}" && \
  GOOS=windows GOARCH=amd64 golangci-lint run --build-tags "${BUILDTAGS}" && \
  GOOS=freebsd GOARCH=amd64 golangci-lint run --build-tags "${BUILDTAGS}" && \
  GOOS=linux GOARCH=arm64 golangci-lint run --build-tags "${BUILDTAGS}" && \
  touch /golangci-lint.done

FROM base as yamllint
RUN --mount=target=/go/src/github.com/moby/buildkit --mount=target=/root/.cache,type=cache \
  yamllint -c .yamllint.yml --strict . && \
  touch /yamllint.done

FROM base as protolint
RUN --mount=target=/go/src/github.com/moby/buildkit \
  protolint lint . && \
  touch /protolint.done

FROM scratch
COPY --link --from=golangci-lint /golangci-lint.done /
COPY --link --from=yamllint /yamllint.done /
COPY --link --from=protolint /protolint.done /
