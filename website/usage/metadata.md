# Metadata

To output build metadata such as the image digest, pass the `--metadata-file` flag.
The metadata will be written as a JSON object to the specified file.
The directory of the specified file must already exist and be writable.

```shell
buildctl build ... --metadata-file metadata.json
```

```json title="jq '.' metadata.json"
{
  "containerimage.config.digest": "sha256:2937f66a9722f7f4a2df583de2f8cb97fc9196059a410e7f00072fc918930e66",
  "containerimage.descriptor": {
    "annotations": {
      "config.digest": "sha256:2937f66a9722f7f4a2df583de2f8cb97fc9196059a410e7f00072fc918930e66",
      "org.opencontainers.image.created": "2022-02-08T21:28:03Z"
    },
    "digest": "sha256:19ffeab6f8bc9293ac2c3fdf94ebe28396254c993aea0b5a542cfb02e0883fa3",
    "mediaType": "application/vnd.oci.image.manifest.v1+json",
    "size": 506
  },
  "containerimage.digest": "sha256:19ffeab6f8bc9293ac2c3fdf94ebe28396254c993aea0b5a542cfb02e0883fa3"
}
```
