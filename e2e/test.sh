#!/bin/bash
set -eu


: ${BUILDCTL=buildctl}
: ${BUILDCTL_CONNECT_RETRIES_MAX=30}
: ${BUILDKITD=buildkitd}
: ${BUILDKITD_FLAGS="--debug"}
: ${ROOTLESSKIT=rootlesskit}

# $tmp holds the following files:
# * pid
# * addr
# * log
tmp=$(mktemp -d /tmp/buildctl-daemonless.XXXXXX)
trap "kill \$(cat $tmp/pid) || true; wait \$(cat $tmp/pid) || true; cat $tmp/log" EXIT
source /workspace/tools.sh
config_dns

# 启动 buildkitd 守护进程
echo "start buildkitd..."
startBuildkitd
waitForBuildkitd


format=oci
random_index=$((RANDOM % 2))
if [ $random_index -eq 0 ];
then
  format=docker
fi

# Loop through all Docker registry addresses, login, build, and push the image
# test.registry.com:4443: custom cert registry
# test.registry.com: http registry on local
# registry.alauda.cn:60080: http registry
# registry.alauda.cn:60070: https registry, http network not allowed, will close socket by server
# build-harbor.alauda.cn: redict https registry, http not found
# localhost localhost registry
# TODO: redict http -> https registry
for REGISTRY in "test.registry.com" "test.registry.com:4443" "registry.alauda.cn:60080" "registry.alauda.cn:60070" "build-harbor.alauda.cn" "localhost"; do
  echo "=> 📢  Building and pull the image from $REGISTRY ..."
  buildctl build \
    --frontend=dockerfile.v0 \
    --opt label:format=${format} \
    --opt label:format_id=${random_index} \
    --opt build-arg:FROM_IMAGE=$REGISTRY \
    --local context=/workspace --local dockerfile=/workspace \
    --output type=${format},dest=./image.tar
  
  rm -f image.tar
  echo "=> ✅ test $REGISTRY success"
done

echo "test pull completed."
