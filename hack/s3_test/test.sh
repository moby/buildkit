#!/bin/sh -ex

/bin/minio server /tmp/data --address=0.0.0.0:9000 --console-address=0.0.0.0:9001 &

while true; do
  curl -s -f http://127.0.0.1:9001 >/dev/null && break
  sleep 1
done

sleep 2
mc alias set myminio http://127.0.0.1:9000 minioadmin minioadmin
mc mb myminio/my-bucket
mc admin trace myminio &

buildkitd -debugaddr 0.0.0.0:8060 &
while true; do
  curl -s -f http://127.0.0.1:8060/debug/pprof/ >/dev/null && break
  sleep 1
done

export default_options="type=s3,bucket=my-bucket,region=us-east-1,endpoint_url=http://127.0.0.1:9000,access_key_id=minioadmin,secret_access_key=minioadmin,use_path_style=true"

rm -rf /tmp/destdir1 /tmp/destdir2

# First build: no cache on s3
# 4 files should be exported (2 blobs + 2 manifests)
buildctl build \
  --progress plain \
  --frontend dockerfile.v0 \
  --local context=/test/test1 \
  --local dockerfile=/test/test1 \
  --import-cache "$default_options,name=foo" \
  --export-cache "$default_options,mode=max,name=bar;foo" \
  --output type=local,dest=/tmp/destdir1

# Check the 5 files are on s3 (3 blobs and 2 manifests)
mc ls --recursive myminio/my-bucket | wc -l | grep 5

# Test the refresh workflow
mc ls --recursive myminio/my-bucket/blobs >/tmp/content
buildctl build \
  --progress plain \
  --frontend dockerfile.v0 \
  --local context=/test/test1 \
  --local dockerfile=/test/test1 \
  --import-cache "$default_options,name=foo" \
  --export-cache "$default_options,mode=max,name=bar;foo"
mc ls --recursive myminio/my-bucket/blobs >/tmp/content2
# No change expected
diff /tmp/content /tmp/content2

sleep 2

buildctl build \
  --progress plain \
  --frontend dockerfile.v0 \
  --local context=/test/test1 \
  --local dockerfile=/test/test1 \
  --import-cache "$default_options,name=foo" \
  --export-cache "$default_options,mode=max,name=bar;foo,touch_refresh=1s"
mc ls --recursive myminio/my-bucket/blobs >/tmp/content2
# Touch refresh = 1 should have caused a change in timestamp
if diff /tmp/content /tmp/content2; then
  exit 1
fi

# Check we can reuse the cache
buildctl prune
buildctl build \
  --progress plain \
  --frontend dockerfile.v0 \
  --local context=/test/test2 \
  --local dockerfile=/test/test2 \
  --import-cache "$default_options,name=foo" \
  --output type=local,dest=/tmp/destdir2 \
  2>&1 | tee /tmp/log

# Check the first step was not executed, but read from S3 cache
cat /tmp/log | grep 'cat /dev/urandom | head -c 100 | sha256sum > unique_first' -A1 | grep CACHED

# Ensure cache is reused
rm /tmp/destdir2/unique_third
diff -r /tmp/destdir1 /tmp/destdir2

# Test the behavior when a blob is missing
mc rm --force --recursive myminio/my-bucket/blobs

buildctl prune
buildctl build \
  --progress plain \
  --frontend dockerfile.v0 \
  --local context=/test/test2 \
  --local dockerfile=/test/test2 \
  --import-cache "$default_options,name=foo" \
  >/tmp/log 2>&1 || true
cat /tmp/log | grep 'NoSuchKey' >/dev/null

echo S3 Checks ok
