# syntax=docker/dockerfile:1.1-experimental

FROM golang:1.14-alpine
RUN  apk add --no-cache git
RUN  go get -u gopkg.in/alecthomas/gometalinter.v1 \
  && mv /go/bin/gometalinter.v1 /go/bin/gometalinter \
  && gometalinter --install
WORKDIR /go/src/github.com/moby/buildkit
RUN --mount=target=/go/src/github.com/moby/buildkit \
	gometalinter --config=gometalinter.json ./...
