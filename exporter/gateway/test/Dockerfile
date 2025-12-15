# syntax=docker/dockerfile-upstream:master

ARG GO_VERSION=1.25
ARG ALPINE_VERSION=3.22
ARG XX_VERSION=1.6.1

# xx is a helper for cross-compilation
FROM --platform=$BUILDPLATFORM tonistiigi/xx:${XX_VERSION} AS xx

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine${ALPINE_VERSION} AS base
RUN apk add git bash
COPY --from=xx / /
WORKDIR /src
ENV GOFLAGS=-mod=vendor

FROM base AS build
RUN apk add --no-cache file
ARG BUILDTAGS=""
ARG TARGETPLATFORM
RUN --mount=target=. --mount=type=cache,target=/root/.cache \
  --mount=target=/go/pkg/mod,type=cache \
  CGO_ENABLED=0 xx-go build -o /test-exporter -tags "$BUILDTAGS netgo static_build osusergo" ./exporter/gateway/test && \
  xx-verify --static /test-exporter

FROM scratch AS release
LABEL moby.buildkit.frontend.network.none="true"
# LABEL moby.buildkit.frontend.caps="" # currently no caps defined
COPY --from=build /test-exporter /bin/test-exporter
ENTRYPOINT ["/bin/test-exporter"]

FROM release
