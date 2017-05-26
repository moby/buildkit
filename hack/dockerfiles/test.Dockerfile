FROM golang:1.8-alpine AS vndr
RUN  apk add --no-cache g++
WORKDIR /go/src/github.com/tonistiigi/buildkit_poc
COPY . .
RUN go test ./...