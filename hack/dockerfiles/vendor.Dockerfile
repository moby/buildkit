# syntax=docker/dockerfile-upstream:master

FROM golang:1.18-alpine AS vendored
RUN  apk add --no-cache git
WORKDIR /src
RUN --mount=target=/src,rw \
  --mount=target=/go/pkg/mod,type=cache \
  go mod tidy && go mod vendor && \
  mkdir /out && cp -r go.mod go.sum vendor /out

FROM scratch AS update
COPY --from=vendored /out /out

FROM vendored AS validate
RUN --mount=target=.,rw \
  git add -A && \
  rm -rf vendor && \
  cp -rf /out/* . && \
  ./hack/validate-vendor check
