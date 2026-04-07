# syntax=docker/dockerfile-upstream:master

ARG NODE_VERSION=24

FROM node:${NODE_VERSION}-alpine AS base
RUN apk add --no-cache git
WORKDIR /src

FROM base AS doctoc

# DOCTOC_VERSION is the version of doctoc to install
# see https://github.com/thlorenz/doctoc/tags for available releases.
ARG DOCTOC_VERSION=v2.3.0
RUN npm install -g doctoc@${DOCTOC_VERSION#v}
RUN --mount=type=bind,source=README.md,target=README.md,rw <<EOT
  set -e
  doctoc README.md
  mkdir /out
  cp README.md /out/
EOT

FROM scratch AS update
COPY --from=doctoc /out /

FROM base AS validate-toc
RUN --mount=type=bind,target=.,rw \
    --mount=type=bind,from=doctoc,source=/out/README.md,target=./README.md <<EOT
  set -e
  if [ -n "$(git status --porcelain -- 'README.md')" ]; then
    echo >&2 'ERROR: The result of "doctoc" differs. Please update with "make doctoc"'
    git diff -- README.md
    exit 1
  fi
EOT
