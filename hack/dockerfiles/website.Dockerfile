# syntax=docker/dockerfile:1

ARG MKDOCS_VERSION="8.1.11"

FROM squidfunk/mkdocs-material:${MKDOCS_VERSION} AS base
RUN apk add --no-cache gcc git git-fast-import musl-dev openssl \
  && pip install --no-cache-dir \
    'lunr' \
    'markdown-include' \
    'mkdocs-awesome-pages-plugin' \
    'mkdocs-exclude' \
    'mkdocs-macros-plugin'

FROM base AS generate
RUN --mount=type=bind,target=. \
  mkdocs build --strict --site-dir /out

FROM scratch AS release
COPY --from=generate /out /
