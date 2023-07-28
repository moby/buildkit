# Build reproducibility

## Reproducing the pinned dependencies

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
      "selector": {
        "identifier": "docker-image://docker.io/library/alpine:latest"
      },
      "updates": {
        "identifier": "docker-image://docker.io/library/alpine:latest@sha256:4edbd2beb5f78b1014028f4fbb99f3237d9561100b6881aabbf5acce2c4f9454"
      }
    },
    {
<<<<<<< HEAD
      "action": "CONVERT",
      "selector": {
        "identifier": "https://raw.githubusercontent.com/moby/buildkit/v0.10.1/README.md"
      },
      "updates": {
        "attrs": {"http.checksum": "sha256:6e4b94fc270e708e1068be28bd3551dc6917a4fc5a61293d51bb36e6b75c4b53"}
      }
=======
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
>>>>>>> origin/v0.10
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
