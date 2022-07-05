# Build reproducibility

## Build dependencies

Build dependencies are generated when your image has been built. These
dependencies include versions of used images, git repositories and HTTP URLs
used by LLB `Source` operation as well as build request attributes.

The structure is base64 encoded and has the following format when decoded:

```json
{
  "frontend": "dockerfile.v0",
  "attrs": {
    "build-arg:foo": "bar",
    "context": "https://github.com/crazy-max/buildkit-buildsources-test.git#master",
    "filename": "Dockerfile",
    "platform": "linux/amd64,linux/arm64",
    "source": "crazymax/dockerfile:master"
  },
  "sources": [
    {
      "type": "docker-image",
      "ref": "docker.io/docker/buildx-bin:0.6.1@sha256:a652ced4a4141977c7daaed0a074dcd9844a78d7d2615465b12f433ae6dd29f0",
      "pin": "sha256:a652ced4a4141977c7daaed0a074dcd9844a78d7d2615465b12f433ae6dd29f0"
    },
    {
      "type": "docker-image",
      "ref": "docker.io/library/alpine:3.13",
      "pin": "sha256:1d30d1ba3cb90962067e9b29491fbd56997979d54376f23f01448b5c5cd8b462"
    },
    {
      "type": "git",
      "ref": "https://github.com/crazy-max/buildkit-buildsources-test.git#master",
      "pin": "259a5aa5aa5bb3562d12cc631fe399f4788642c1"
    },
    {
      "type": "http",
      "ref": "https://raw.githubusercontent.com/moby/moby/v20.10.21/README.md",
      "pin": "sha256:419455202b0ef97e480d7f8199b26a721a417818bc0e2d106975f74323f25e6c"
    }
  ]
}
```

* `frontend` defines the frontend used to build.
* `attrs` defines build request attributes.
* `sources` defines build sources.
  * `type` defines the source type (`docker-image`, `git` or `http`).
  * `ref` is the reference of the source.
  * `pin` is the source digest.
* `deps` defines build dependencies of input contexts.

### Image config

A new field similar to the one for inline cache has been added to the image
configuration to embed build dependencies:

```json
{
  "moby.buildkit.buildinfo.v0": "<base64>"
}
```

By default, the build dependencies are inlined in the image configuration. You
can disable this behavior with the [`buildinfo` attribute](../README.md#imageregistry).

### Exporter response (metadata)

The solver response (`ExporterResponse`) also contains a new key
`containerimage.buildinfo` with the same structure as image config encoded in
base64:

```json
{
  "ExporterResponse": {
    "containerimage.buildinfo": "<base64>",
    "containerimage.digest": "sha256:..."
  }
}
```

If multi-platforms are specified, they will be suffixed with the corresponding
platform:

```json
{
  "ExporterResponse": {
    "containerimage.buildinfo/linux/amd64": "<base64>",
    "containerimage.buildinfo/linux/arm64": "<base64>",
    "containerimage.digest": "sha256:..."
  }
}
```

### Metadata JSON output

If you're using the `--metadata-file` flag with [`buildctl`](../README.md#metadata),
[`buildx build`](https://github.com/docker/buildx/blob/master/docs/reference/buildx_build.md)
or [`buildx bake`](https://github.com/docker/buildx/blob/master/docs/reference/buildx_bake.md):

```shell
jq '.' metadata.json
```
```json
{
  "containerimage.buildinfo": {
    "frontend": "dockerfile.v0",
    "attrs": {
      "context": "https://github.com/crazy-max/buildkit-buildsources-test.git#master",
      "filename": "Dockerfile",
      "source": "docker/dockerfile:master"
    },
    "sources": [
      {
        "type": "docker-image",
        "ref": "docker.io/docker/buildx-bin:0.6.1@sha256:a652ced4a4141977c7daaed0a074dcd9844a78d7d2615465b12f433ae6dd29f0",
        "pin": "sha256:a652ced4a4141977c7daaed0a074dcd9844a78d7d2615465b12f433ae6dd29f0"
      },
      {
        "type": "docker-image",
        "ref": "docker.io/library/alpine:3.13",
        "pin": "sha256:026f721af4cf2843e07bba648e158fb35ecc876d822130633cc49f707f0fc88c"
      }
    ]
  },
  "containerimage.digest": "sha256:..."
}
```

### Reproducing the pinned dependencies

<!-- TODO: s/master/v0.11/ after the release -->
Reproducing the pinned dependencies is supported in the master branch of BuildKit.

e.g.,
```bash
buildctl build --frontend dockerfile.v0 --local dockerfile=. --local context=. --source-policy-file Dockerfile.pol
```

An example `Dockerfile.pol`:
```json
{
    "sources": [
      {
        "type": "docker-image",
        "ref": "docker.io/library/alpine:latest",
        "pin": "sha256:4edbd2beb5f78b1014028f4fbb99f3237d9561100b6881aabbf5acce2c4f9454"
      },
      {
        "type": "http",
        "ref": "https://raw.githubusercontent.com/moby/buildkit/v0.10.1/README.md",
        "pin": "sha256:6e4b94fc270e708e1068be28bd3551dc6917a4fc5a61293d51bb36e6b75c4b53"
      }
    ]
}
```

* `sources`: a subset of the `."containerimage.buildinfo".sources` property of the `metadata.json`

Reproduction is currently supported for the following source types:
* `docker-image`
* `http`
<!-- TODO: git -->
