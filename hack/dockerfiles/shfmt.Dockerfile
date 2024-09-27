# syntax=docker/dockerfile-upstream:master@sha256:df54e73548d586209f6fc6d34d61edf8277e1b9d2704aff8fe75294a17c6a29b

FROM mvdan/shfmt:v3.1.2-alpine AS shfmt
WORKDIR /src
ARG SHFMT_FLAGS="-i 2 -ci"

FROM shfmt AS generate
WORKDIR /out/hack
RUN --mount=target=/src \
  cp -a /src/hack/. ./ && \
  shfmt -l -w $SHFMT_FLAGS .

FROM scratch AS update
COPY --from=generate /out /

FROM shfmt AS validate
RUN --mount=target=. \
  shfmt $SHFMT_FLAGS -d ./hack
