FROM golang:1.9-alpine AS vndr
RUN  apk add --no-cache git
ARG VNDR_VERSION=master
RUN go get -d github.com/LK4D4/vndr \
  && cd /go/src/github.com/LK4D4/vndr \
	&& git checkout $VNDR_VERSION \
	&& go install ./
WORKDIR /go/src/github.com/moby/buildkit
COPY . .
RUN vndr --verbose