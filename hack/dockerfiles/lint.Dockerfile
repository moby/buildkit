# syntax=docker/dockerfile-upstream:master

ARG GO_VERSION=1.20

FROM golang:${GO_VERSION}-alpine AS base
ENV GOFLAGS="-buildvcs=false"
RUN apk add --no-cache gcc musl-dev yamllint
RUN wget -O- -nv https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s v1.52.2
WORKDIR /go/src/github.com/moby/buildkit

FROM base as golangci-lint
ARG BUILDTAGS
RUN --mount=target=/go/src/github.com/moby/buildkit --mount=target=/root/.cache,type=cache,sharing=locked \
  GOARCH=amd64 golangci-lint run --build-tags "${BUILDTAGS}" && \
  GOARCH=arm64 golangci-lint run --build-tags "${BUILDTAGS}" && \
  touch /golangci-lint.done

FROM base as yamllint
RUN --mount=target=/go/src/github.com/moby/buildkit --mount=target=/root/.cache,type=cache \
  yamllint -c .yamllint.yml --strict . && \
  touch /yamllint.done

FROM scratch
COPY --link --from=golangci-lint /golangci-lint.done /
COPY --link --from=yamllint /yamllint.done /
