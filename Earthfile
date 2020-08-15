
build:
    FROM DOCKERFILE --target buildkit-buildkitd-linux .
    ARG EARTHLY_TARGET_TAG
    ARG EARTHLY_TARGET_TAG_DOCKER=$EARTHLY_TARGET_TAG
    ARG TAG=$EARTHLY_TARGET_TAG_DOCKER
    SAVE IMAGE --push earthly/buildkit:$TAG
