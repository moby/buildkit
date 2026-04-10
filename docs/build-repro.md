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
      "action": "CONVERT",
      "selector": {
        "identifier": "https://raw.githubusercontent.com/moby/buildkit/v0.10.1/README.md"
      },
      "updates": {
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

When the caller does not pass `SOURCE_DATE_EPOCH`, the Dockerfile may define a
default in the global scope:

```dockerfile
ARG SOURCE_DATE_EPOCH=1704067200
FROM alpine
```

```console
buildctl build --frontend dockerfile.v0 --opt build-arg:SOURCE_DATE_EPOCH=$(git log -1 --pretty=%ct) ...
```
The `buildctl` CLI (>= v0.13) and Docker Buildx (>= 0.10) automatically propagate the `$SOURCE_DATE_EPOCH` environment value from the client host to the `SOURCE_DATE_EPOCH` build arg.

The build arg value is used for:
- the `created` timestamp in the [OCI Image Config](https://github.com/opencontainers/image-spec/blob/main/config.md#properties)
- the `created` timestamp in the `history` objects in the [OCI Image Config](https://github.com/opencontainers/image-spec/blob/main/config.md#properties)
- the `org.opencontainers.image.created` annotation in the [OCI Image Index](https://github.com/opencontainers/image-spec/blob/main/annotations.md#pre-defined-annotation-keys)
- the timestamp of the files exported with the `local` exporter
- the timestamp of the files exported with the `tar` exporter

To apply the build arg value to the timestamps of the files inside the image, specify `rewrite-timestamp=true` as an image exporter option:
```
--output type=image,name=docker.io/username/image,push=true,rewrite-timestamp=true
```

The `rewrite-timestamp` option is available since BuildKit v0.13.
See [v0.12 documentation](https://github.com/moby/buildkit/blob/v0.12/docs/build-repro.md#caveats) for dealing with timestamps
in BuildKit v0.12 and v0.11.

See also the [documentation](/frontend/dockerfile/docs/reference.md#buildkit-built-in-build-args) of the Dockerfile frontend.

## `compatibility-version`

`compatibility-version` pins digest-affecting image assembly behavior for the `image` and `oci` exporters.

BuildKit currently supports these values:

- `10` for the `v0.13.0` and `v0.14.0` historical path
- `20` for the `v0.15.0+` path and current behavior

### `20`

`20` is the current compatibility version.

It matches released BuildKit output from `v0.15.0` and newer for the compatibility test matrix in `client/compatibility_test.go`.

### `10`

`10` represents the `v0.13.0` and `v0.14.0` historical path.

Compared to `20`, `10` differs in two ways:

- in the git-backed copied-content layer, non-executable regular files from the `git` source are stored as mode `0666` instead of `0644`
- `compression=zstd` produces different historical artifacts

The git-backed layer boundary was introduced by commit `6493fd064ceece1d29f3e61aa73a531502f4795c` ("git: ensure exec option is propagated to child git clis").

The old pre-`v0.15.0` zstd artifacts crossed two independent historical changes:

1. `a2a440feb58388e6873082900a5c0b35746b18f3` ("vendor: update klauspost/compress to v1.17.9")
2. `6493fd064ceece1d29f3e61aa73a531502f4795c` ("git: ensure exec option is propagated to child git clis")

The first changed zstd-compressed layer bytes. The second changed the git-backed copied-content layer metadata described above.

Because the currently supported historical backfill for `10` only covers the git-backed artifact difference, BuildKit currently rejects `compression=zstd` with `compatibility-version=10` instead of claiming full reproduction of the old zstd output.
