FROM golang:1.12-alpine AS update
RUN  apk add --no-cache git
WORKDIR /src
COPY . .
RUN go mod tidy && go mod vendor && rm -rf /go/pkg/mod && ln -s /src /out

FROM update AS validate
RUN ./hack/validate-vendor check
