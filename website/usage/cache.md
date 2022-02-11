# Cache

To show local build cache (`/var/lib/buildkit`):

```shell
buildctl du -v
```

To prune local build cache:

```shell
buildctl prune
```

## Garbage collection

See [`daemon configuration`](daemon-configuration.md) page.

## Export cache

BuildKit supports the following cache exporters:

* `inline`: embed the cache into the image, and push them to the registry together
* `registry`: push the image and the cache separately
* `local`: export to a local directory
* `gha`: export to GitHub Actions cache

In most case you want to use the `inline` cache exporter.
However, note that the `inline` cache exporter only supports `min` cache mode.
To enable `max` cache mode, push the image and the cache separately by using `registry` cache exporter.

`inline` and `registry` exporters both store the cache in the registry. For importing the cache, `type=registry` is sufficient for both, as specifying the cache format is not necessary.

### Inline (push image and cache together)

```shell
buildctl build ... \
  --output type=image,name=docker.io/username/image,push=true \
  --export-cache type=inline \
  --import-cache type=registry,ref=docker.io/username/image
```

Note that the inline cache is not imported unless [`--import-cache type=registry,ref=...`](#registry-push-image-and-cache-separately)
is provided.

Inline cache embeds cache metadata into the image config. The layers in the
image will be left untouched compared to the image with no cache information.

!!! warning
    Docker-integrated BuildKit (`DOCKER_BUILDKIT=1 docker build`) and `docker buildx` requires
    `--build-arg BUILDKIT_INLINE_CACHE=1` to be specified to enable the `inline` cache exporter.
    
    However, the standalone `buildctl` does NOT require `--opt build-arg:BUILDKIT_INLINE_CACHE=1` and the build-arg is simply ignored.

### Registry (push image and cache separately)

```bash
buildctl build ... \
  --output type=image,name=localhost:5000/myrepo:image,push=true \
  --export-cache type=registry,ref=localhost:5000/myrepo:buildcache \
  --import-cache type=registry,ref=localhost:5000/myrepo:buildcache
```

#### `--export-cache` options

* `type=registry`
* `mode=min` (default): only export layers for the resulting image
* `mode=max`: export all the layers of all intermediate steps.
* `ref=docker.io/user/image:tag`: reference
* `oci-mediatypes=true|false`: whether to use OCI mediatypes in exported manifests. Since BuildKit `v0.8` defaults to true.

#### `--import-cache` options

* `type=registry`
* `ref=docker.io/user/image:tag`: reference

### Local directory

```bash
buildctl build ... --export-cache type=local,dest=path/to/output-dir
buildctl build ... --import-cache type=local,src=path/to/input-dir
```

The directory layout conforms to OCI Image Spec v1.0.

#### `--export-cache` options

* `type=local`
* `mode=min` (default): only export layers for the resulting image
* `mode=max`: export all the layers of all intermediate steps.
* `dest=path/to/output-dir`: destination directory for cache exporter
* `oci-mediatypes=true|false`: whether to use OCI mediatypes in exported manifests. Since BuildKit `v0.8` defaults to true.

#### `--import-cache` options

* `type=local`
* `src=path/to/input-dir`: source directory for cache importer
* `digest=sha256:deadbeef`: digest of the manifest list to import.
* `tag=customtag`: custom tag of image. Defaults "latest" tag digest in `index.json` is for digest, not for tag

### GitHub Actions cache

!!! experimental "Experimental"
    This feature is considered **EXPERIMENTAL** and under active development until further notice.

```shell
buildctl build ... \
  --output type=image,name=docker.io/username/image,push=true \
  --export-cache type=gha \
  --import-cache type=gha
```

Github Actions cache saves both cache metadata and layers to GitHub's Cache
service. This cache currently has a [size limit of 10GB](https://docs.github.com/en/actions/advanced-guides/caching-dependencies-to-speed-up-workflows#usage-limits-and-eviction-policy)
that is shared accross different caches in the repo. If you exceed this limit,
GitHub will save your cache but will begin evicting caches until the total
size is less than 10 GB. Recycling caches too often can result in slower
runtimes overall.

Similarly to using [actions/cache](https://github.com/actions/cache), caches are [scoped by branch](https://docs.github.com/en/actions/advanced-guides/caching-dependencies-to-speed-up-workflows#restrictions-for-accessing-a-cache), with the default and target branches being available to every branch.

Following attributes are required to authenticate against the [Github Actions Cache service API](https://github.com/tonistiigi/go-actions-cache/blob/master/api.md#authentication):
* `url`: Cache server URL (default `$ACTIONS_CACHE_URL`)
* `token`: Access token (default `$ACTIONS_RUNTIME_TOKEN`)

!!! info
    This type of cache can be used with [Docker Build Push Action](https://github.com/docker/build-push-action)
    where `url` and `token` will be automatically set. To use this backend in a inline `run` step, you have to include [crazy-max/ghaction-github-runtime](https://github.com/crazy-max/ghaction-github-runtime)
    in your workflow to expose the runtime.

#### `--export-cache` options

* `type=gha`
* `mode=min` (default): only export layers for the resulting image
* `mode=max`: export all the layers of all intermediate steps.
* `scope=buildkit`: which scope cache object belongs to (default `buildkit`)

#### `--import-cache` options

* `type=gha`
* `scope=buildkit`: which scope cache object belongs to (default `buildkit`)
