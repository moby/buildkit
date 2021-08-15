
FROM alpine:3.13
WORKDIR /buildkit

build:
    ARG DOCKER_TARGET=buildkit-buildkitd-linux
    FROM DOCKERFILE --target $DOCKER_TARGET .

code:
    COPY . .
    SAVE ARTIFACT /buildkit

image:
    FROM +build
    SAVE IMAGE earthly/raw-buildkitd:latest
