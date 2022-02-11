# Output

By default, the build result and intermediate cache will only remain internally
in BuildKit. An output needs to be specified to retrieve the result.

## Image/Registry

```shell
buildctl build ... --output type=image,name=docker.io/username/image,push=true
```

!!! note
    To export the cache embed with the image and pushing them to registry together,
    type `registry` is required to import the cache, you should specify
    `--export-cache type=inline` and `--import-cache type=registry,ref=...`. To
    export the cache to a local directy, you should specify `--export-cache type=local`.

    Details in [Export cache](cache.md#export-cache).

```shell
buildctl build ...\
  --output type=image,name=docker.io/username/image,push=true \
  --export-cache type=inline \
  --import-cache type=registry,ref=docker.io/username/image
```

Keys supported by image output:

* `name=[value]`: image name
* `push=true`: push after creating the image
* `push-by-digest=true`: push unnamed image
* `registry.insecure=true`: push to insecure HTTP registry
* `oci-mediatypes=true`: use OCI mediatypes in configuration JSON instead of Docker's
* `unpack=true`: unpack image after creation (for use with containerd)
* `dangling-name-prefix=[value]`: name image with `prefix@<digest>` , used for anonymous images
* `name-canonical=true`: add additional canonical name `name@<digest>`
* `compression=[uncompressed,gzip,estargz,zstd]`: choose compression type for layers newly created and cached, gzip is default value. estargz should be used with `oci-mediatypes=true`.
* `compression-level=[value]`: compression level for gzip, estargz (0-9) and zstd (0-22)
* `force-compression=true`: forcefully apply `compression` option to all layers (including already existing layers).
* `buildinfo=[all,imageconfig,metadata,none]`: choose [build dependency](../user-guides/build-reproducibility.md#build-dependencies) version to export (default `all`).

!!! note
    If credentials are required, `buildctl` will attempt to read Docker configuration
    file `$DOCKER_CONFIG/config.json`. `$DOCKER_CONFIG` defaults to `~/.docker`.

## Local directory

The local client will copy the files directly to the client. This is useful if
BuildKit is being used for building something else than container images.

```shell
buildctl build ... --output type=local,dest=path/to/output-dir
```

To export specific files use multi-stage builds with a scratch stage and copy
the needed files into that stage with `COPY --from`.

```dockerfile
...
FROM scratch as testresult

COPY --from=builder /usr/src/app/testresult.xml .
...
```

```shell
buildctl build ... --opt target=testresult --output type=local,dest=path/to/output-dir
```

Tar exporter is similar to local exporter but transfers the files through a tarball.

```shell
buildctl build ... --output type=tar,dest=out.tar
buildctl build ... --output type=tar > out.tar
```

## Docker tarball

```shell
# exported tarball is also compatible with OCI spec
buildctl build ... --output type=docker,name=myimage | docker load
```

## OCI tarball

```shell
buildctl build ... --output type=oci,dest=path/to/output.tar
buildctl build ... --output type=oci > output.tar
```

### Containerd image store

The containerd worker needs to be used

```shell
buildctl build ... --output type=image,name=docker.io/username/image
ctr --namespace=buildkit images ls
```

!!! note
    To change the containerd namespace, you need to change `worker.containerd.namespace`
    in [`daemon configuration`](daemon-configuration.md) page.
