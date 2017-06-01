FROM golang:1.8-alpine AS runc
RUN apk add --no-cache g++ linux-headers
RUN apk add --no-cache git make
ARG RUNC_VERSION=v1.0.0-rc3
RUN git clone https://github.com/opencontainers/runc.git "$GOPATH/src/github.com/opencontainers/runc" \
	&& cd "$GOPATH/src/github.com/opencontainers/runc" \
	&& git checkout -q "$RUNC_VERSION" \
	&& go build -o /usr/bin/runc ./

FROM golang:1.8-alpine
RUN  apk add --no-cache g++ linux-headers
COPY --from=runc /usr/bin/runc /usr/bin/runc 
WORKDIR /go/src/github.com/tonistiigi/buildkit_poc
COPY . .


# EXPORT COPY --from=% /go/src/github.com/opencontainers/runc/runc /usr/local/bin/docker-runc