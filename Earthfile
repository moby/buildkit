
FROM alpine:3.13
WORKDIR /buildkit

build:
    FROM DOCKERFILE --target buildkit-buildkitd-linux .

code:
    COPY . .
    SAVE ARTIFACT /buildkit

image:
    FROM +build
    SAVE IMAGE earthly/raw-buildkitd:latest
