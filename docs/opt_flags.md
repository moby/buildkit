# Flags for 
## Build buildctl --opt 
This page documents the build options defined by Dockerfile frontend. When using buildctl these options can be passed when running buildctl build --frontend dockerfile.v0 --opt ... . When you are implementing your own frontend you should use the same option names to make sure equivalent docker buildx(link) CLI flags configure your frontend properly.
## Resources:
#### _Issue_: https://github.com/moby/buildkit/pull/3068
#### _Template:_ https://github.com/docker/buildx/blob/master/docs/reference/buildx_build.md

---------------------------------------------------------------------------------------------------------------------
## Dockerfile Frontend Build Options
##### use with buildctl build --opt
    "context" 
    "dockerfile" 
    "target" 
    "filename" 
    "cache-from"    // for registry only. deprecated in favor of keyCacheImports 
    "cache-imports" // JSON representation of []CacheOptionsEntry 
    "build-arg:BUILDKIT_CACHE_MOUNT_NS" 
    "Dockerfile" 
    ".dockerignore" 
    "build-arg:" 
    "label:" 
    "no-cache" 
    "platform" 
    "multi-platform" 
    "image-resolve-mode" 
    "add-hosts" 
    "force-network-mode" 
    "override-copy-image" // remove after CopyOp implemented 
    "contextkey" 
    "dockerfilekey" 
    "contextsubdir" 
    "build-arg:BUILDKIT_CONTEXT_KEEP_GIT_DIR" 
# Flags:    
| Name                                       | Default   | Description          
|:-------------------------------------------|:---------|:-------------------------|
        |context                            | 'context'     | define additional build context with specified contents. In Dockerfile the context can be accessed when FROM name or --from=name is used. When Dockerfile defines a stage with the same name it is overwritten. When using multiple contexts, use ":" to name the different contexts |
        | dockerfile                        | "dockerfile"  | The default name of the Dockerfile. 
        |target                             |               | The build stage to build. If not specified, all stages will be built. |
        |filename                           | "Dockerfile"  | The name of the Dockerfile.
        |cache-imports                      |               | JSON representation of an array of CacheOptionsEntry, which represents cache import|
        |sources                            |               | The location of external Dockerfile frontend image that is used to build the Dockerfile  Each entry specifies the Type, Source, and Target of the cache import. See the documentation for CacheOptionsEntry for more information. Deprecated cache-from in favor of cache-imports. |
        |build-arg:BUILDKIT_CACHE_MOUNT_NS  |               | The namespace of the cache mount.|
        |defaultDockerfileName              |"Dockerfile"   | The default name of the Dockerfile. |
        |.dockerignore                      |".dockerignore"| The name of the .dockerignore file. |
        |build-arg                          |               | The prefix for build arguments.  |
        |label                              | "label:"      | The prefix for labels. |
        |no-cache                           |               | Disables the build cache. If present, the build will not use any cache from previous builds. |
        |platform                           |               | The target platform for the build. If not specified, the host platform will be used.|
        |multi-platform                     |               | Enables multi-platform builds.|
        |image-resolve-mode                 | "cached"      | The image resolve mode. It can be either "cached", "manifest", or "fallback". |
        |add-hosts                          |               | A list of hosts to add to /etc/hosts in the build containers. It should be in the format "hostname:IP". |
        |force-network-mode                 |               | Forces the use of network mode for the build.|
        |override-copy-image                |               | Deprecated. Remove after CopyOp is implemented.|
        |contextkey                         |               | The key used to store the context directory in the build cache.|
        |dockerfilekey                      |               | The key used to store the Dockerfile in the build cache. |
        |contextsubdir                      | "."           | The subdirectory of the context directory to use as the build context. |
        |build-arg:BUILDKIT_CONTEXT_KEEP_GIT_DIR |          | A build argument that determines whether to keep the .git directory in the build context. If set to "1", the .git directory will be kept.|
        
## Examples: 
Example:
    Container Image: context:alpine=docker-image://alpine
    Git: context=git@github.com:moby/buildkit,``
    Local source directory: context=.,context:mydir=/path/to/dir
    URL: context=https://github.com/moby/moby.git
    Full command example:
```
buildctl build \
        --frontend=dockerfile.v0 \
        --local dockerfile=. \
        --local context=. \
        --local mydir_local_is_tmp=/tmp \
        --opt context:image-base=docker-image://alpine \
        --opt context:mydir=local:mydir_local_is_tmp \
        --opt context:git-base=git@github.com:moby/buildkit \
        --opt context:http-base=https://github.com/moby/moby.git 

FROM image-base
COPY --from=mydir myfile /
FROM git-base
FROM http-base 
```

## dockerfile-specific options
To use the build contexts, pass --opt context:<source>=<target>, where the <source> is the name in the dockerfile, and <target> is a properly formatted target. These can be the following:

    --opt context:alpine=foo1 - replace usage of alpine with a named context foo1, that already should have been loaded via --local.
    --opt context:alpine=foo2@sha256:bd04a5b26dec16579cd1d7322e949c5905c4742269663fcbc84dcb2e9f4592fb - replace usage of alpine with the image or index whose sha256 hash is bd04a5b26dec16579cd1d7322e949c5905c4742269663fcbc84dcb2e9f4592fb from an OCI layout whose named context foo2, that already should have been loaded via --oci-layout.
    --opt context:alpine=docker-image://docker.io/library/ubuntu:latest - replace usage of alpine with the docker image docker.io/library/ubuntu:latest from the registry.
    --opt context:alpine=https://example.com/foo/bar.git - replace usage of alpine with the contents of the git repository at https://example.com/foo/bar.git
