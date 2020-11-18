FROM ubuntu:20.04
ENV DEBIAN_FRONTEND=noninteractive 
RUN apt-get update && apt-get upgrade -y
RUN apt-get install wget git software-properties-common build-essential -y
RUN wget https://golang.org/dl/go1.14.9.linux-amd64.tar.gz
RUN tar -xvf go1.14.9.linux-amd64.tar.gz
RUN mv go /usr/local

RUN mkdir /root/src
ENV GOPATH /root/go
ENV GOROOT /usr/local/go
ENV PATH $PATH:/usr/local/go/bin

RUN go get github.com/moby/buildkit
RUN go get -u github.com/dvyukov/go-fuzz/go-fuzz github.com/dvyukov/go-fuzz/go-fuzz-build
COPY fuzz.go /go/src/github.com/moby/buildkit/frontend/dockerfiles/parser/
RUN cd /go/src/github.com/moby/buildkit/frontend/dockerfiles/parser && $GOPATH/bin/go-fuzz-build && $GOPATH/bin/go-fuzz
