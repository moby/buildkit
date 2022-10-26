#!/bin/bash -ex

function cleanup() {
  docker rmi moby/buildkit:azblobtest
}

trap cleanup EXIT
cd "$(dirname "$0")"

docker buildx bake --load

AZURE_ACCOUNT_NAME=azblobcacheaccount
AZURE_ACCOUNT_URL=azblobcacheaccount.blob.localhost.com
AZURE_ACCOUNT_KEY=$(echo "azblobcacheaccountkey" | base64)

docker run \
  --rm \
  --privileged \
  --add-host ${AZURE_ACCOUNT_URL}:127.0.0.1 \
  -e AZURE_ACCOUNT_NAME=${AZURE_ACCOUNT_NAME} \
  -e AZURE_ACCOUNT_KEY=${AZURE_ACCOUNT_KEY} \
  -e AZURE_ACCOUNT_URL=${AZURE_ACCOUNT_URL} \
  moby/buildkit:azblobtest \
  /test/test.sh
