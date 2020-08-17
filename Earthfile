
build:
    FROM DOCKERFILE --target buildkit-buildkitd-linux .
    ARG EARTHLY_TARGET_TAG_DOCKER
    ARG TAG=$EARTHLY_TARGET_TAG_DOCKER
    SAVE IMAGE --push earthly/buildkit:$TAG
