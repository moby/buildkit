#!/bin/bash -ex

function cleanup() {
  docker rmi moby/buildkit:ipfs-cluster
}

trap cleanup EXIT
cd "$(dirname "$0")"

docker buildx bake --load

docker run \
  --rm \
  --privileged \
  moby/buildkit:ipfs-cluster \
  /test/test.sh
