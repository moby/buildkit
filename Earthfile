
FROM alpine:3.13
WORKDIR /buildkit

build:
    FROM DOCKERFILE --target buildkit-buildkitd-linux .
    ARG EARTHLY_TARGET_TAG_DOCKER
    ARG TAG=$EARTHLY_TARGET_TAG_DOCKER

code:
    COPY . .
    SAVE ARTIFACT /buildkit
