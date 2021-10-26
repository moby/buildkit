# Build reproducibility

## Build dependencies

Build dependencies are generated when your image has been built. These
dependencies include versions of used images, git repositories and HTTP URLs
used by LLB `Source` operation.

By default, the build dependencies are embedded in the image configuration and
also available in the solver response. The export mode can be refined with
the [`buildinfo` attribute](../README.md#imageregistry).

### Image config

A new field similar to the one for inline cache has been added to the image
configuration to embed build dependencies:

```text
"moby.buildkit.buildinfo.v1": <base64>
```

The structure is base64 encoded and has the following format when decoded:

```json
{
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
      "ref": "https://raw.githubusercontent.com/moby/moby/master/README.md",
      "pin": "sha256:419455202b0ef97e480d7f8199b26a721a417818bc0e2d106975f74323f25e6c"
    }
  ]
}
```

* `type` defines the source type (`docker-image`, `git` or `http`).
* `ref` is the reference of the source.
* `pin` is the source digest.

### Exporter response (metadata)

The solver response (`ExporterResponse`) also contains a new key
`containerimage.buildinfo` with the same structure as image config encoded in
base64:

```json
{
  "ExporterResponse": {
    "containerimage.buildinfo": "<base64>",
    "containerimage.digest": "sha256:...",
    "image.name": "..."
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
    "containerimage.digest": "sha256:...",
    "image.name": "..."
  }
}
```
