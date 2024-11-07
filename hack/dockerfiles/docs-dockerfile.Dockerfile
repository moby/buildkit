# syntax=docker/dockerfile:1

ARG GO_VERSION=1.23
ARG ALPINE_VERSION=3.20

FROM golang:${GO_VERSION}-alpine${ALPINE_VERSION} AS golatest

FROM golatest AS docsgen
WORKDIR /src
ENV CGO_ENABLED=0
RUN --mount=target=. \
  --mount=target=/root/.cache,type=cache \
  --mount=target=/go/pkg/mod,type=cache \
  go build -mod=vendor -o /out/docsgen ./frontend/dockerfile/linter/generate.go

FROM alpine AS gen
RUN apk add --no-cache rsync git
WORKDIR /src
COPY --from=docsgen /out/docsgen /usr/bin
RUN --mount=target=/context \
  --mount=target=.,type=tmpfs <<EOT
set -ex
rsync -a /context/. .
cd frontend/dockerfile/linter
docsgen ./dist
mkdir /out
cp -r dist/* /out
EOT

FROM scratch AS update
COPY --from=gen /out /

FROM gen AS validate
RUN --mount=target=/context \
  --mount=target=.,type=tmpfs <<EOT
set -e
rsync -a /context/. .
git add -A
rm -rf frontend/dockerfile/docs/rules/*
cp -rf /out/* ./frontend/dockerfile/docs/rules/
if [ -n "$(git status --porcelain -- frontend/dockerfile/docs/rules/)" ]; then
  echo >&2 'ERROR: Dockerfile docs result differs. Please update with "make docs-dockerfile"'
  git status --porcelain -- frontend/dockerfile/docs/rules/
  exit 1
fi
EOT
