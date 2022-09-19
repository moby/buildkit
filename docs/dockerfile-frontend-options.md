# Dockerfile Frontend Build Options

## use with `buildctl build --opt`
For `dockerfile.v0` frontend there are some parameters that can be used with `buildctl build --opt`.
The following parameters are the most frequently used.
* target: set the target build stage to build.
  * Example: `target=Stage1`
* filename: name of the Dockerfile (Default is 'Dockerfile'),set to use a differently-named Dockerfile.
  * Example: `filename=dockerfile.prod`,`filename=other.dockerfile`
* dockerfilekey: name of the custom named context for a directory which includes Dockerfile. Useful when there is more than one build source.
  * Example: `dockerfilekey=dockerfile2`
  * Full command example: 
    ```shell
    buildctl build \ 
        --frontend=dockerfile.v0 \
        --local dockerfile=. \
        --local dockerfile2=/tmp \
        --opt dockerfilekey=dockerfile2
    ```
    buildctl will try to use the `/tmp/Dockerfile` as Dockerfile.
* context: define additional build context with specified contents. In Dockerfile the context can be accessed when `FROM` name or `--from=name` is used. When Dockerfile defines a stage with the same name it is overwritten.
  * When using multiple contexts, use ":" to name the different contexts
  * Container Image: `context:alpine=docker-image://alpine`
  * Git: `context=git@github.com:moby/buildkit`,``
  * Local source directory: `context=.`,`context:mydir=/path/to/dir`
  * URL: `context=https://github.com/moby/moby.git` 
  * Full command example:
    ```shell
    buildctl build \
        --frontend=dockerfile.v0 \
        --local dockerfile=. \
        --local context=. \
        --local mydir_local_is_tmp=/tmp \
        --opt context:image-base=docker-image://alpine \
        --opt context:mydir=local:mydir_local_is_tmp \
        --opt context:git-base=git@github.com:moby/buildkit \
        --opt context:http-base=https://github.com/moby/moby.git
    ```
    ```dockerfile
    FROM image-base
    COPY --from=mydir myfile /
    FROM git-base
    FROM http-base
    ```
* source: the location of the Dockerfile syntax that is used to build the Dockerfile
  * Example `source=docker/dockerfile:1` ,it same as `# syntax=docker/dockerfile:1` in Dockerfile
  * Another example:
    ```
    buildctl build --frontend=dockerfile.v0 \
      --opt context="https://github.com/crazy-max/buildkit-buildsources-test.git#master" \
      --opt filename="Dockerfile" \
      --opt source="crazymax/dockerfile:master"
    ```
  * See also [Building a Dockerfile using external frontend](#building-a-dockerfile-using-external-frontend) and [dockerfile frontend reference](https://github.com/moby/buildkit/blob/master/frontend/dockerfile/docs/reference.md#syntax).
* platform: use for building multi-platform image, this options is a comma-separated list.
  * Example: `platform=linux/amd64,linux/arm64`
  * See [Building multi-platform images](https://github.com/moby/buildkit/blob/master/docs/multi-platform.md) for more details.
* build-arg: pass [`ARG`](https://docs.docker.com/engine/reference/builder/#arg) value when build time. 
  * Example: `build-arg:APT_MIRROR=cdn-fastly.deb.debian.org`,`"build-arg:foo=bar"`
  * Also used for some buildkit's internal control logic.See [BuildKit built-in build args](https://github.com/moby/buildkit/blob/master/frontend/dockerfile/docs/reference.md#buildkit-built-in-build-args) for more details.Example: `build-arg:BUILDKIT_INLINE_CACHE=1`
* label: use for add label metadata for image.
  * Example: `label:bar: baz`
  * See [Docker object labels | Docker Documentation](https://docs.docker.com/config/labels-custom-metadata/) for best practices.

There are also some more advanced parameters that can be referred to.Please refer to the [source code](https://github.com/moby/buildkit/blob/master/frontend/dockerfile/builder/build.go) for the fields.