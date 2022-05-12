#!/bin/sh -ex

cd "$(dirname "$0")"

docker buildx bake --load
docker run --rm --privileged -p 9001:9001 -p 8060:8060 moby/buildkit:s3test /test/test.sh
docker rmi moby/buildkit:s3test
