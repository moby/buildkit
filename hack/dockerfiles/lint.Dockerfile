# syntax=docker/dockerfile-upstream:master

ARG GO_VERSION=1.23
ARG ALPINE_VERSION=3.20
ARG XX_VERSION=1.5.0
ARG PROTOLINT_VERSION=0.50.5
ARG GOLANGCI_LINT_VERSION=1.61.0
ARG GOPLS_VERSION=v0.26.0
# GOPLS_ANALYZERS defines gopls analyzers to be run. disabled by default: deprecated unusedvariable simplifyrange
ARG GOPLS_ANALYZERS="embeddirective fillreturns infertypeargs nonewvars norangeoverfunc noresultvalues simplifycompositelit simplifyslice undeclaredname unusedparams useany"

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine${ALPINE_VERSION} AS golang-base
FROM --platform=$BUILDPLATFORM yoheimuta/protolint:${PROTOLINT_VERSION} AS protolint-base
FROM --platform=$BUILDPLATFORM tonistiigi/xx:${XX_VERSION} AS xx

FROM golang-base AS base
ENV GOFLAGS="-buildvcs=false"
ARG GOLANGCI_LINT_VERSION
RUN apk add --no-cache gcc musl-dev yamllint
RUN wget -O- -nv https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s v${GOLANGCI_LINT_VERSION}
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
  

FROM base AS golangci-verify
RUN --mount=target=/go/src/github.com/moby/buildkit \
  golangci-lint config verify && \
  touch /golangci-verify.done

FROM base AS yamllint
RUN --mount=target=/go/src/github.com/moby/buildkit \
  yamllint -c .yamllint.yml --strict . && \
  touch /yamllint.done

FROM base AS protolint
RUN --mount=target=/go/src/github.com/moby/buildkit \
  protolint lint . && \
  touch /protolint.done

FROM golang-base AS gopls
RUN apk add --no-cache git
ARG GOPLS_VERSION
WORKDIR /src
RUN git clone https://github.com/golang/tools.git && \
  cd tools && git checkout ${GOPLS_VERSION}
WORKDIR tools/gopls
ARG GOPLS_ANALYZERS
RUN <<'EOF'
  set -ex
  mkdir -p /out
  for analyzer in ${GOPLS_ANALYZERS}; do
    mkdir -p internal/cmd/$analyzer
    cat <<eot > internal/cmd/$analyzer/main.go
package main

import (
	"golang.org/x/tools/go/analysis/singlechecker"
	analyzer "golang.org/x/tools/gopls/internal/analysis/$analyzer"
)

func main() { singlechecker.Main(analyzer.Analyzer) }
eot
    echo "Analyzing with ${analyzer}..."
    go build -o /out/$analyzer ./internal/cmd/$analyzer
  done
EOF

FROM golang-base AS gopls-analyze
COPY --link --from=xx / /
ARG GOPLS_ANALYZERS
ARG TARGETNAME
ARG TARGETPLATFORM
WORKDIR /go/src/github.com/moby/buildkit
RUN --mount=target=. \
  --mount=target=/root/.cache,type=cache,id=lint-cache-${TARGETNAME}-${TARGETPLATFORM} \
  --mount=target=/gopls-analyzers,from=gopls,source=/out <<EOF
  set -ex
  xx-go --wrap
  for analyzer in ${GOPLS_ANALYZERS}; do
    go vet -vettool=/gopls-analyzers/$analyzer ./...
  done
EOF


FROM scratch
COPY --link --from=golangci-lint /golangci-lint.done /
COPY --link --from=golangci-verify /golangci-verify.done /
COPY --link --from=yamllint /yamllint.done /
COPY --link --from=protolint /protolint.done /
