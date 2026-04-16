# syntax=docker/dockerfile-upstream:master

ARG GO_VERSION=1.26
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
    EXACT_TAG=$(git describe --exact-match --tags --match "dockerfile/[0-9]*" 2>/dev/null || true)
  else
    VERSION=$(git describe --always --tags --match "dockerfile/[0-9]*-$CHANNEL")
    EXACT_TAG=$(git describe --exact-match --tags --match "dockerfile/[0-9]*-$CHANNEL" 2>/dev/null || true)
  fi
  BUILTIN_VERSION=$(sed -n 's/^const version = "\(.*\)"$/\1/p' ./frontend/dockerfile/version/version.go)
  case "$BUILTIN_VERSION" in
    *.*.*) EXPECTED_VERSION="$BUILTIN_VERSION" ;;
    *.*) EXPECTED_VERSION="$BUILTIN_VERSION.0" ;;
    *)
      echo "invalid frontend/dockerfile/version/version.go version: $BUILTIN_VERSION"
      exit 1
      ;;
  esac
  case "$EXACT_TAG" in
    dockerfile/*)
      ACTUAL_VERSION=${EXACT_TAG#dockerfile/}
      ACTUAL_VERSION=${ACTUAL_VERSION%%-*}
      if [ "$ACTUAL_VERSION" != "$EXPECTED_VERSION" ]; then
        echo "frontend/dockerfile/version/version.go version $BUILTIN_VERSION does not match external frontend version $EXACT_TAG"
        exit 1
      fi
      ;;
  esac
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
