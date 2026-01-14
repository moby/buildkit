# syntax=docker/dockerfile-upstream:master

ARG GO_VERSION=1.25
ARG ALPINE_VERSION=3.23
ARG XX_VERSION=1.9.0

# xx is a helper for cross-compilation
FROM --platform=$BUILDPLATFORM tonistiigi/xx:${XX_VERSION} AS xx

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine${ALPINE_VERSION} AS base
RUN apk add git bash
COPY --from=xx / /
WORKDIR /src
ENV GOFLAGS=-mod=vendor

FROM base AS version
ARG CHANNEL=mainline
ARG BUILDTAGS
RUN --mount=target=. <<EOT
  set -e
  tagsFile="./frontend/dockerfile/release/$CHANNEL/tags"
  if [ ! -f "$tagsFile" ]; then
    echo "No build tags found for $CHANNEL."
    exit 1
  fi
  if [ "$CHANNEL" = "mainline" ]; then
    VERSION=$(git describe --always --tags --match "dockerfile/[0-9]*")
  else
    VERSION=$(git describe --always --tags --match "dockerfile/[0-9]*-$CHANNEL")
  fi
  PKG=github.com/moby/buildkit/frontend/dockerfile/cmd/dockerfile-frontend
  REVISION=$(git rev-parse HEAD)$(if ! git diff --no-ext-diff --quiet --exit-code; then echo .m; fi)
  echo "-X main.Version=${VERSION} -X main.Revision=${REVISION} -X main.Package=${PKG}" | tee /tmp/.ldflags
  echo -n "$BUILDTAGS $(cat $tagsFile)" | tee /tmp/.buildtags
EOT

FROM base AS build
RUN apk add --no-cache file
ARG TARGETPLATFORM
RUN --mount=target=. --mount=type=cache,target=/root/.cache \
  --mount=target=/go/pkg/mod,type=cache \
  --mount=source=/tmp/.ldflags,target=/tmp/.ldflags,from=version \
  --mount=source=/tmp/.buildtags,target=/tmp/.buildtags,from=version <<EOT
  set -e
  ldflags=$(cat /tmp/.ldflags)
  buildtags=$(cat /tmp/.buildtags)
  set -x
  CGO_ENABLED=0 xx-go build -o /dockerfile-frontend -ldflags "-d $ldflags" -tags "$buildtags netgo static_build osusergo" ./frontend/dockerfile/cmd/dockerfile-frontend
  xx-verify --static /dockerfile-frontend
EOT

FROM scratch AS release
LABEL moby.buildkit.frontend.network.none="true"
LABEL moby.buildkit.frontend.caps="moby.buildkit.frontend.inputs,moby.buildkit.frontend.subrequests,moby.buildkit.frontend.contexts,moby.buildkit.frontend.gitquerystring"
COPY --from=build /dockerfile-frontend /bin/dockerfile-frontend
ENTRYPOINT ["/bin/dockerfile-frontend"]

FROM release
