#!/bin/bash -ex

# Start buildkitd
buildkitd &

# Wait for buildkitd to be ready
try=0
max=10
until buildctl debug workers >/dev/null 2>&1; do
  if [ $try -gt $max ]; then
    echo >&2 "buildkitd not ready after $max trials"
    exit 1
  fi
  sleep $(awk "BEGIN{print (100 + $try * 20) * 0.001}")
  try=$(expr $try + 1)
done

export default_options="type=ipfs,cluster_api=192.168.65.2:9094,daemon_api=192.168.65.2:5001,pin_name=buildkit-cache,mfs_dir=/buildkit-cache"

rm -rf /tmp/destdir1 /tmp/destdir2

# First build of test1: no cache
buildctl build \
  --progress plain \
  --frontend dockerfile.v0 \
  --local context=/test/test1 \
  --local dockerfile=/test/test1 \
  --export-cache "$default_options,mode=max" \
  --output type=local,dest=/tmp/destdir1

echo "Sleeping 3s to make sure files are available in IPFS..."
sleep 3s

ipfs --api /ip4/192.168.65.2/tcp/5001 files ls -l /buildkit-cache
ipfs --api /ip4/192.168.65.2/tcp/5001 files ls -l /buildkit-cache/manifests
ipfs --api /ip4/192.168.65.2/tcp/5001 files ls -l /buildkit-cache/blobs

# Check the 4 blob files and 1 manifest file in the /buildkit-cache directory of the MFS of IPFS
blobCount=$(ipfs --api /ip4/192.168.65.2/tcp/5001 files ls -l /buildkit-cache/blobs | wc -l)
if (("$blobCount" != 4)); then
  echo "unexpected number of blobs found: $blobCount"
  exit 1
fi

manifestCount=$(ipfs --api /ip4/192.168.65.2/tcp/5001 files ls -l /buildkit-cache/manifests | wc -l)
if (("$manifestCount" != 1)); then
  echo "unexpected number of manifests found: $manifestCount"
  exit 1
fi

mkdir /tmp/content1
blobsDirCID=$(ipfs --api /ip4/192.168.65.2/tcp/5001 files stat /buildkit-cache/blobs --hash)
ipfs --api /ip4/192.168.65.2/tcp/5001 get $blobsDirCID -o /tmp/content1

# Second build of test1: Test that cache was used
buildctl build \
  --progress plain \
  --frontend dockerfile.v0 \
  --local context=/test/test1 \
  --local dockerfile=/test/test1 \
  --import-cache "$default_options" \
  2>&1 | tee /tmp/log1

echo "Sleeping 3s to make sure files are available in IPFS..."
sleep 3s

# Check that the existing steps were read from the cache
cat /tmp/log1 | grep 'cat /dev/urandom | head -c 100 | sha256sum > unique_first' -A1 | grep CACHED
cat /tmp/log1 | grep 'cat /dev/urandom | head -c 100 | sha256sum > unique_second' -A1 | grep CACHED

# No change expected in the blobs
mkdir /tmp/content2
blobsDirCID=$(ipfs --api /ip4/192.168.65.2/tcp/5001 files stat /buildkit-cache/blobs --hash)
ipfs --api /ip4/192.168.65.2/tcp/5001 get $blobsDirCID -o /tmp/content2
diff -r /tmp/content1 /tmp/content2

# First build of test2: Test that we can reuse the cache for a different docker image
buildctl prune
buildctl build \
  --progress plain \
  --frontend dockerfile.v0 \
  --local context=/test/test2 \
  --local dockerfile=/test/test2 \
  --import-cache "$default_options" \
  --output type=local,dest=/tmp/destdir2 \
  2>&1 | tee /tmp/log2

echo "Sleeping 3s to make sure files are available in IPFS..."
sleep 3s

mkdir /tmp/content3
blobsDirCID=$(ipfs --api /ip4/192.168.65.2/tcp/5001 files stat /buildkit-cache/blobs --hash)
ipfs --api /ip4/192.168.65.2/tcp/5001 get $blobsDirCID -o /tmp/content3

## TODO
## There should ONLY be 1 difference between the contents of /tmp/content1 and /tmp/content3
## This difference is that in /tmp/content3 there should 1 extra blob corresponding to the layer: RUN cat /dev/urandom | head -c 100 | sha256sum > unique_third
#contentDiff=$(diff -r /tmp/content1 /tmp/content3 || :)
#if [[ ! "$contentDiff" =~ ^"Only in /tmp/content3/blobs: sha256:"[a-z0-9]{64}$ ]]; then
#  echo "unexpected diff found $contentDiff"
#  exit 1
#fi

# Check the existing steps were not executed, but read from cache
cat /tmp/log2 | grep 'cat /dev/urandom | head -c 100 | sha256sum > unique_first' -A1 | grep CACHED

# Ensure cache is reused
rm /tmp/destdir2/unique_third
diff -r /tmp/destdir1 /tmp/destdir2

# Second build of test2: Test the behavior when a blob is missing
ipfs --api /ip4/192.168.65.2/tcp/5001 files rm -rf /buildkit-cache/blobs

echo "Sleeping 3s to make sure blobs are removed from IPFS..."
sleep 3s

buildctl prune
buildctl build \
  --progress plain \
  --frontend dockerfile.v0 \
  --local context=/test/test2 \
  --local dockerfile=/test/test2 \
  --import-cache "$default_options" \
  2>&1 | tee /tmp/log3

cat /tmp/log3 | grep -E 'blob.+not found' >/dev/null

echo IPFS checks ok
