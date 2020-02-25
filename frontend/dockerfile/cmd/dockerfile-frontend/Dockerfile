# syntax = docker/dockerfile:1.0-experimental

FROM --platform=$BUILDPLATFORM tonistiigi/xx:golang@sha256:6f7d999551dd471b58f70716754290495690efa8421e0a1fcf18eb11d0c0a537 AS xgo

FROM --platform=$BUILDPLATFORM golang:1.14-buster AS base
COPY --from=xgo / /
WORKDIR /src
ENV GOFLAGS=-mod=vendor

FROM base AS version
ARG CHANNEL
# TODO: PKG should be inferred from go modules
RUN --mount=target=. \ 
  PKG=github.com/moby/buildkit/frontend/dockerfile/cmd/dockerfile-frontend VERSION=$(./frontend/dockerfile/cmd/dockerfile-frontend/hack/detect "$CHANNEL") REVISION=$(git rev-parse HEAD)$(if ! git diff --no-ext-diff --quiet --exit-code; then echo .m; fi); \
  echo "-X main.Version=${VERSION} -X main.Revision=${REVISION} -X main.Package=${PKG}" | tee /tmp/.ldflags; \
  echo -n "${VERSION}" | tee /tmp/.version;

FROM base AS build
RUN apt-get update && apt-get install -y --no-install-recommends file
ARG BUILDTAGS=""
ARG TARGETPLATFORM
ENV TARGETPLATFORM=$TARGETPLATFORM
RUN --mount=target=. --mount=type=cache,target=/root/.cache \
  --mount=target=/go/pkg/mod,type=cache \
  --mount=source=/tmp/.ldflags,target=/tmp/.ldflags,from=version \
  CGO_ENABLED=0 go build -o /dockerfile-frontend -ldflags "-d $(cat /tmp/.ldflags)" -tags "$BUILDTAGS netgo static_build osusergo" ./frontend/dockerfile/cmd/dockerfile-frontend && \
  file /dockerfile-frontend | grep "statically linked"

FROM scratch AS release
LABEL moby.buildkit.frontend.network.none="true"
COPY --from=build /dockerfile-frontend /bin/dockerfile-frontend
ENTRYPOINT ["/bin/dockerfile-frontend"]


FROM base AS buildid-check
RUN apt-get update && apt-get install -y jq
COPY /frontend/dockerfile/cmd/dockerfile-frontend/hack/check-daily-outdated .
COPY --from=r.j3ss.co/reg /usr/bin/reg /bin
COPY --from=build /dockerfile-frontend .
ARG CHANNEL
ARG REPO
ARG DATE
RUN ./check-daily-outdated $CHANNEL $REPO $DATE /out

FROM scratch AS buildid
COPY --from=buildid-check /out/ /

FROM release
