# syntax=docker/dockerfile-upstream:master

# xx is a helper for cross-compilation
FROM --platform=$BUILDPLATFORM tonistiigi/xx:master@sha256:d4254d9739ce2de9fb88e09bdc716aa0c65f0446a2a2143399f991d71136a3d4 AS xx

FROM --platform=$BUILDPLATFORM golang:1.19-alpine AS base
RUN apk add git bash
COPY --from=xx / /
WORKDIR /src
ENV GOFLAGS=-mod=vendor

FROM base AS version
ARG CHANNEL
# TODO: PKG should be inferred from go modules
RUN --mount=target=. \ 
  PKG=github.com/moby/buildkit/frontend/dockerfile/cmd/dockerfile-frontend VERSION=$(./frontend/dockerfile/cmd/dockerfile-frontend/hack/detect "$CHANNEL") REVISION=$(git rev-parse HEAD)$(if ! git diff --no-ext-diff --quiet --exit-code; then echo .m; fi) \
  && echo "-X main.Version=${VERSION} -X main.Revision=${REVISION} -X main.Package=${PKG}" | tee /tmp/.ldflags \
  && echo -n "${VERSION}" | tee /tmp/.version

FROM base AS build
RUN apk add --no-cache file
ARG BUILDTAGS=""
ARG TARGETPLATFORM
RUN --mount=target=. --mount=type=cache,target=/root/.cache \
  --mount=target=/go/pkg/mod,type=cache \
  --mount=source=/tmp/.ldflags,target=/tmp/.ldflags,from=version \
  CGO_ENABLED=0 xx-go build -o /dockerfile-frontend -ldflags "-d $(cat /tmp/.ldflags)" -tags "$BUILDTAGS netgo static_build osusergo" ./frontend/dockerfile/cmd/dockerfile-frontend && \
  xx-verify --static /dockerfile-frontend

FROM scratch AS release
LABEL moby.buildkit.frontend.network.none="true"
LABEL moby.buildkit.frontend.caps="moby.buildkit.frontend.inputs,moby.buildkit.frontend.subrequests,moby.buildkit.frontend.contexts"
COPY --from=build /dockerfile-frontend /bin/dockerfile-frontend
ENTRYPOINT ["/bin/dockerfile-frontend"]


FROM base AS buildid-check
RUN apt-get update && apt-get --no-install-recommends install -y jq
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
