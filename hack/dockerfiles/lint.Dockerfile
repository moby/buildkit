# syntax=docker/dockerfile-upstream:master

ARG GO_VERSION=1.27
ARG ALPINE_VERSION=3.24
ARG XX_VERSION=1.10.0
ARG PROTOLINT_VERSION=0.56.4
ARG GOLANGCI_LINT_VERSION=v2.14.0
ARG GOLANGCI_FROM_SOURCE=false
ARG GOPLS_ANALYZERS_VERSION=v0.3.0

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine${ALPINE_VERSION} AS golang-base
FROM --platform=$BUILDPLATFORM yoheimuta/protolint:${PROTOLINT_VERSION} AS protolint-base
FROM --platform=$BUILDPLATFORM tonistiigi/xx:${XX_VERSION} AS xx


FROM golang-base AS golangci-build
WORKDIR /src
ARG GOLANGCI_LINT_VERSION
ADD https://github.com/golangci/golangci-lint.git#${GOLANGCI_LINT_VERSION} .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/ go mod download
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/ mkdir -p out && go build -o /out/golangci-lint ./cmd/golangci-lint


FROM scratch AS golangci-binary-false
FROM scratch AS golangci-binary-true
COPY --from=golangci-build /out/golangci-lint golangci-lint
FROM golangci-binary-${GOLANGCI_FROM_SOURCE} AS golangci-binary

FROM golang-base AS gopls-analyzers
ARG GOPLS_ANALYZERS_VERSION
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/ \
  GOBIN=/out go install github.com/crazy-max/gopls-analyzers/cmd/gopls-analyzers@${GOPLS_ANALYZERS_VERSION}

FROM golang-base AS base
ENV GOFLAGS="-buildvcs=false"
RUN apk add --no-cache gcc musl-dev yamllint
ARG GOLANGCI_LINT_VERSION
ARG GOLANGCI_FROM_SOURCE
COPY --link --from=golangci-binary / /usr/bin/
RUN [ "${GOLANGCI_FROM_SOURCE}" = "true" ] && exit 0; wget -O- -nv https://raw.githubusercontent.com/golangci/golangci-lint/${GOLANGCI_LINT_VERSION}/install.sh | sh -s ${GOLANGCI_LINT_VERSION}
COPY --link --from=protolint-base /usr/local/bin/protolint /usr/local/bin/protolint
COPY --link --from=xx / /
WORKDIR /go/src/github.com/moby/buildkit

FROM base AS golangci-lint
ARG TARGETNAME
ARG BUILDTAGS
ARG TARGETPLATFORM
RUN --mount=target=/go/src/github.com/moby/buildkit \
    --mount=target=/root/.cache,type=cache,id=lint-cache-${TARGETNAME}-${TARGETPLATFORM} \
  xx-go --wrap && \
  golangci-lint run --build-tags "${BUILDTAGS}" && \
  touch /golangci-lint.done
  

FROM base AS golangci-verify-false
RUN --mount=target=/go/src/github.com/moby/buildkit \
  golangci-lint config verify && \
  touch /golangci-verify.done

FROM scratch AS golangci-verify-true
COPY <<EOF /golangci-verify.done
EOF

FROM golangci-verify-${GOLANGCI_FROM_SOURCE} AS golangci-verify

FROM base AS yamllint
RUN --mount=target=/go/src/github.com/moby/buildkit \
  yamllint -c .yamllint.yml --strict . && \
  touch /yamllint.done

FROM base AS protolint
RUN --mount=target=/go/src/github.com/moby/buildkit \
  protolint lint . && \
  touch /protolint.done

FROM golang-base AS gopls-analyze
COPY --link --from=xx / /
ARG TARGETNAME
ARG TARGETPLATFORM
WORKDIR /go/src/github.com/moby/buildkit
RUN --mount=target=. \
  --mount=target=/root/.cache,type=cache,id=lint-cache-${TARGETNAME}-${TARGETPLATFORM} \
  --mount=target=/usr/bin/gopls-analyzers,from=gopls-analyzers,source=/out/gopls-analyzers <<EOF
  set -ex
  xx-go --wrap
  gopls-analyzers -list-analyzers
  # The flate sources are copied from Go and use a nonstandard generated marker.
  go vet -vettool=/usr/bin/gopls-analyzers -verbose -exclude-generated -exclude='/util/compression/go126flate/' ./...
EOF

FROM golang-base AS gopls-fix-run
RUN apk add --no-cache git
COPY --link --from=xx / /
ARG TARGETNAME
ARG TARGETPLATFORM
WORKDIR /go/src/github.com/moby/buildkit
RUN --mount=target=.,rw \
  --mount=target=/root/.cache,type=cache,id=lint-cache-${TARGETNAME}-${TARGETPLATFORM} \
  --mount=target=/usr/bin/gopls-analyzers,from=gopls-analyzers,source=/out/gopls-analyzers <<EOF
  set -ex
  xx-go --wrap
  mkdir /out
  /usr/bin/gopls-analyzers -verbose -fix -exclude-generated -exclude='/util/compression/go126flate/' ./...
  for file in $(git status --porcelain | awk '/^ M/ {print $2}'); do
    mkdir -p /out/$(dirname $file)
    cp $file /out/$file
  done
EOF

FROM scratch AS gopls-fix
COPY --link --from=gopls-fix-run /out /

FROM scratch
COPY --link --from=golangci-lint /golangci-lint.done /
COPY --link --from=golangci-verify /golangci-verify.done /
COPY --link --from=yamllint /yamllint.done /
COPY --link --from=protolint /protolint.done /
