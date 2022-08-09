#!/bin/bash -ex

# Refer to https://docs.microsoft.com/en-us/azure/storage/common/storage-use-azurite Azurite documentation
rm -rf /tmp/azurite

export AZURITE_ACCOUNTS="${AZURE_ACCOUNT_NAME}:${AZURE_ACCOUNT_KEY}"
BLOB_PORT=10000

azurite --silent --location /tmp/azurite --debug /tmp/azurite/azurite.debug --blobPort ${BLOB_PORT} &
timeout 15 bash -c "until echo > /dev/tcp/localhost/${BLOB_PORT}; do sleep 0.5; done"

buildkitd -debugaddr 0.0.0.0:8060 &
while true; do
  curl -s -f http://127.0.0.1:8060/debug/pprof/ >/dev/null && break
  sleep 1
done

export default_options="type=azblob,container=cachecontainer,account_url=http://${AZURE_ACCOUNT_URL}:${BLOB_PORT},secret_access_key=${AZURE_ACCOUNT_KEY}"

rm -rf /tmp/destdir1 /tmp/destdir2

# First build of test1: no cache
buildctl build \
  --progress plain \
  --frontend dockerfile.v0 \
  --local context=/test/test1 \
  --local dockerfile=/test/test1 \
  --import-cache "$default_options,name=foo" \
  --export-cache "$default_options,mode=max,name=bar;foo" \
  --output type=local,dest=/tmp/destdir1

# Check the 4 blob files and 2 manifest files in the azure blob container
blobCount=$(az storage blob list --output tsv --prefix blobs --container-name cachecontainer --connection-string "DefaultEndpointsProtocol=http;AccountName=${AZURE_ACCOUNT_NAME};AccountKey=${AZURE_ACCOUNT_KEY};BlobEndpoint=http://${AZURE_ACCOUNT_URL}:${BLOB_PORT};" | wc -l)
if (("$blobCount" != 4)); then
  echo "unexpected number of blobs found: $blobCount"
  exit 1
fi

manifestCount=$(az storage blob list --output tsv --prefix manifests --container-name cachecontainer --connection-string "DefaultEndpointsProtocol=http;AccountName=${AZURE_ACCOUNT_NAME};AccountKey=${AZURE_ACCOUNT_KEY};BlobEndpoint=http://${AZURE_ACCOUNT_URL}:${BLOB_PORT};" | wc -l)
if (("$manifestCount" != 2)); then
  echo "unexpected number of manifests found: $manifestCount"
  exit 1
fi

mkdir /tmp/content1
az storage blob download-batch -d /tmp/content1 --pattern blobs/* -s cachecontainer --connection-string "DefaultEndpointsProtocol=http;AccountName=${AZURE_ACCOUNT_NAME};AccountKey=${AZURE_ACCOUNT_KEY};BlobEndpoint=http://${AZURE_ACCOUNT_URL}:${BLOB_PORT};"

# Second build of test1: Test that cache was used
buildctl build \
  --progress plain \
  --frontend dockerfile.v0 \
  --local context=/test/test1 \
  --local dockerfile=/test/test1 \
  --import-cache "$default_options,name=foo" \
  --export-cache "$default_options,mode=max,name=bar;foo" \
  2>&1 | tee /tmp/log1

# Check that the existing steps were read from the cache
cat /tmp/log1 | grep 'cat /dev/urandom | head -c 100 | sha256sum > unique_first' -A1 | grep CACHED
cat /tmp/log1 | grep 'cat /dev/urandom | head -c 100 | sha256sum > unique_second' -A1 | grep CACHED

# No change expected in the blobs
mkdir /tmp/content2
az storage blob download-batch -d /tmp/content2 --pattern blobs/* -s cachecontainer --connection-string "DefaultEndpointsProtocol=http;AccountName=${AZURE_ACCOUNT_NAME};AccountKey=${AZURE_ACCOUNT_KEY};BlobEndpoint=http://${AZURE_ACCOUNT_URL}:${BLOB_PORT};"
diff -r /tmp/content1 /tmp/content2

# First build of test2: Test that we can reuse the cache for a different docker image
buildctl prune
buildctl build \
  --progress plain \
  --frontend dockerfile.v0 \
  --local context=/test/test2 \
  --local dockerfile=/test/test2 \
  --import-cache "$default_options,name=foo" \
  --export-cache "$default_options,mode=max,name=bar;foo" \
  --output type=local,dest=/tmp/destdir2 \
  2>&1 | tee /tmp/log2

mkdir /tmp/content3
az storage blob download-batch -d /tmp/content3 --pattern blobs/* -s cachecontainer --connection-string "DefaultEndpointsProtocol=http;AccountName=${AZURE_ACCOUNT_NAME};AccountKey=${AZURE_ACCOUNT_KEY};BlobEndpoint=http://${AZURE_ACCOUNT_URL}:${BLOB_PORT};"

# There should ONLY be 1 difference between the contents of /tmp/content1 and /tmp/content3
# This difference is that in /tmp/content3 there should 1 extra blob corresponding to the layer: RUN cat /dev/urandom | head -c 100 | sha256sum > unique_third
contentDiff=$(diff -r /tmp/content1 /tmp/content3 || :)
if [[ ! "$contentDiff" =~ ^"Only in /tmp/content3/blobs: sha256:"[a-z0-9]{64}$ ]]; then
  echo "unexpected diff found $contentDiff"
  exit 1
fi

# Check the existing steps were not executed, but read from cache
cat /tmp/log2 | grep 'cat /dev/urandom | head -c 100 | sha256sum > unique_first' -A1 | grep CACHED

# Ensure cache is reused
rm /tmp/destdir2/unique_third
diff -r /tmp/destdir1 /tmp/destdir2

# Second build of test2: Test the behavior when a blob is missing
az storage blob delete-batch -s cachecontainer --pattern blobs/* --connection-string "DefaultEndpointsProtocol=http;AccountName=${AZURE_ACCOUNT_NAME};AccountKey=${AZURE_ACCOUNT_KEY};BlobEndpoint=http://${AZURE_ACCOUNT_URL}:${BLOB_PORT};"

buildctl prune
buildctl build \
  --progress plain \
  --frontend dockerfile.v0 \
  --local context=/test/test2 \
  --local dockerfile=/test/test2 \
  --import-cache "$default_options,name=foo" \
  2>&1 | tee /tmp/log3

cat /tmp/log3 | grep -E 'blob.+not found' >/dev/null

pids=""

for i in $(seq 0 9); do
  buildctl build \
    --progress plain \
    --frontend dockerfile.v0 \
    --local context=/test/test1 \
    --local dockerfile=/test/test1 \
    --import-cache "$default_options,name=foo" \
    --export-cache "$default_options,mode=max,name=bar;foo" \
    &>/tmp/concurrencytestlog$i &
  pids="$pids $!"
done

wait $pids

for i in $(seq 0 9); do
  cat /tmp/concurrencytestlog$i | grep -q -v 'failed to upload blob '
done

echo Azure blob checks ok
