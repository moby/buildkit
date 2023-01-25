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

Reproducing the pinned dependencies is supported since BuildKit v0.11.

e.g.,
```bash
buildctl build --frontend dockerfile.v0 --local dockerfile=. --local context=. --source-policy-file policy.json
```

An example `policy.json`:
```json
{
  "rules": [
    {
      "action": "CONVERT",
      "source": {
        "type": "docker-image",
        "identifier": "docker.io/library/alpine:latest"
      },
      "destination": {
        "identifier": "docker-image://docker.io/library/alpine:latest@sha256:4edbd2beb5f78b1014028f4fbb99f3237d9561100b6881aabbf5acce2c4f9454"
      }
    },
    {
      "action": "CONVERT",
      "source": {
        "type": "http",
        "identifier": "https://raw.githubusercontent.com/moby/buildkit/v0.10.1/README.md"
      },
      "destination": {
        "attrs": {"http.checksum": "sha256:6e4b94fc270e708e1068be28bd3551dc6917a4fc5a61293d51bb36e6b75c4b53"}
      }
    }
  ]
}
```

Any source type is supported, but how to pin a source depends on the type.

## `SOURCE_DATE_EPOCH`
[`SOURCE_DATE_EPOCH`](https://reproducible-builds.org/docs/source-date-epoch/) is the convention for pinning timestamps to a specific value.

The Dockerfile frontend supports consuming the `SOURCE_DATE_EPOCH` value as a special build arg, since BuildKit 0.11.
Minimal support is also available on older BuildKit when using Dockerfile 1.5 frontend.

```console
buildctl build --frontend dockerfile.v0 --opt build-arg:SOURCE_DATE_EPOCH=$(git log -1 --pretty=%ct) ...
```

The `buildctl` CLI does not automatically propagate the `$SOURCE_DATE_EPOCH` environment value from the client host to the `SOURCE_DATE_EPOCH` build arg.
However, higher level build tools, such as Docker Buildx (>= 0.10), may automatically capture the environment value.

The build arg value is used for:
- the `created` timestamp in the [OCI Image Config](https://github.com/opencontainers/image-spec/blob/main/config.md#properties)
- the `created` timestamp in the `history` objects in the [OCI Image Config](https://github.com/opencontainers/image-spec/blob/main/config.md#properties)
- the `org.opencontainers.image.created` annotation in the [OCI Image Index](https://github.com/opencontainers/image-spec/blob/main/annotations.md#pre-defined-annotation-keys)
- the timestamp of the files exported with the `local` exporter
- the timestamp of the files exported with the `tar` exporter

The build arg value is not used for the timestamps of the files inside the image currently ([Caveats](#caveats)).

See also the [documentation](/frontend/dockerfile/docs/reference.md#buildkit-built-in-build-args) of the Dockerfile frontend.

## Caveats
### Timestamps of the files inside the image
Currently, the `SOURCE_DATE_EPOCH` value is not used for the timestamps of the files inside the image.

Workaround:
```dockerfile
# Limit the timestamp upper bound to SOURCE_DATE_EPOCH.
# Workaround for https://github.com/moby/buildkit/issues/3180
ARG SOURCE_DATE_EPOCH
RUN find $( ls / | grep -E -v "^(dev|mnt|proc|sys)$" ) -newermt "@${SOURCE_DATE_EPOCH}" -writable -xdev | xargs touch --date="@${SOURCE_DATE_EPOCH}" --no-dereference
```

The `touch` command above is [not effective](https://github.com/moby/buildkit/issues/3309) for mount point directories.
A workaround is to create mount point directories below `/dev` (tmpfs) so that the mount points will not be included in the image layer.

### Timestamps of whiteouts
Currently, the `SOURCE_DATE_EPOCH` value is not used for the timestamps of "whiteouts" that are created on removing files.

Workaround:
```dockerfile
# Squash the entire stage for resetting the whiteout timestamps.
# Workaround for https://github.com/moby/buildkit/issues/3168
FROM scratch
COPY --from=0 / /
```

The timestamps of the regular files in the original stage are maintained in the squashed stage, so you do not need to touch the files after this `COPY` instruction.
