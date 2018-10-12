# syntax = tonistiigi/dockerfile:runmount20181002

FROM --platform=$BUILDPLATFORM golang:1.11 AS builder
RUN apt-get update && apt-get install -y --no-install-recommends file
ARG BUILDTAGS=""
ARG TARGETOS
ARG TARGETARCH
ENV GOOS=$TARGETOS GOARCH=$TARGETARCH
WORKDIR /go/src/github.com/moby/buildkit
RUN --mount=target=. --mount=type=cache,target=/root/.cache \
  CGO_ENABLED=0 go build -o /dockerfile-frontend -tags "$BUILDTAGS" --ldflags '-extldflags "-static"' ./frontend/dockerfile/cmd/dockerfile-frontend && \
  file /dockerfile-frontend | grep "statically linked"

FROM scratch
COPY --from=builder /dockerfile-frontend /bin/dockerfile-frontend
ENTRYPOINT ["/bin/dockerfile-frontend"]