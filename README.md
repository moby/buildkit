### Important: This repository is in an early development phase

[![asciicinema example](https://asciinema.org/a/gPEIEo1NzmDTUu2bEPsUboqmU.png)](https://asciinema.org/a/gPEIEo1NzmDTUu2bEPsUboqmU)


## BuildKit

<!-- godoc is mainly for LLB stuff -->
[![GoDoc](https://godoc.org/github.com/moby/buildkit?status.svg)](https://godoc.org/github.com/moby/buildkit/client/llb)
[![Build Status](https://travis-ci.org/moby/buildkit.svg?branch=master)](https://travis-ci.org/moby/buildkit)
[![Go Report Card](https://goreportcard.com/badge/github.com/moby/buildkit)](https://goreportcard.com/report/github.com/moby/buildkit)


BuildKit is a toolkit for converting source code to build artifacts in an efficient, expressive and repeatable manner.

Key features:
- Automatic garbage collection
- Extendable frontend formats
- Concurrent dependency resolution
- Efficient instruction caching
- Build cache import/export
- Nested build job invocations
- Distributable workers
- Multiple output formats
- Pluggable architecture


#### Quick start

Dependencies:
- [runc](https://github.com/opencontainers/runc)
- [containerd](https://github.com/containerd/containerd) (if you want to use containerd worker)


The following command installs `buildd` and `buildctl` to `/usr/local/bin`:

```bash
$ make && sudo make install
```

You can also use `make binaries-all` to prepare `buildd.containerd_only` and `buildd.oci_only`.

`examples/buildkit*` directory contains scripts that define how to build different configurations of BuildKit and its dependencies using the `client` package. Running one of these script generates a protobuf definition of a build graph. Note that the script itself does not execute any steps of the build.

You can use `buildctl debug dump-llb` to see what data is in this definition. Add `--dot` to generate dot layout.

```bash
go run examples/buildkit0/buildkit.go | buildctl debug dump-llb | jq .
```

To start building use `buildctl build` command. The example script accepts `--target` flag to choose between `containerd-worker-only` and `oci-worker-only` configurations. In OCI worker mode BuildKit binaries are built together with `runc`. In containerd worker mode, the `containerd` binary is built as well from the upstream repo.

```bash
go run examples/buildkit0/buildkit.go | buildctl build
```

`buildctl build` will show interactive progress bar by default while the build job is running. It will also show you the path to the trace file that contains all information about the timing of the individual steps and logs.

Different versions of the example scripts show different ways of describing the build definition for this project to show the capabilities of the library. New versions have been added when new features have become available.

- `./examples/buildkit0` - uses only exec operations, defines a full stage per component.
- `./examples/buildkit1` - cloning git repositories has been separated for extra concurrency.
- `./examples/buildkit2` - uses git sources directly instead of running `git clone`, allowing better performance and much safer caching.
- `./examples/buildkit3` - allows using local source files for separate components eg. `./buildkit3 --runc=local | buildctl build --local runc-src=some/local/path`  
- `./examples/dockerfile2llb` - can be used to convert a Dockerfile to LLB for debugging purposes
- `./examples/gobuild` - shows how to use nested invocation to generate LLB for Go package internal dependencies


#### Examples

##### Starting the buildd daemon:

```
buildd --debug --root /var/lib/buildkit
```

The buildd daemon suppports two worker backends: OCI (runc) and containerd.

By default, the OCI (runc) worker is used.
You can set `--oci-worker=false --containerd-worker=true` to use the containerd worker.

We are open to add more backends.

##### Building a Dockerfile:

```
buildctl build --frontend=dockerfile.v0 --local context=. --local dockerfile=.
buildctl build --frontend=dockerfile.v0 --local context=. --local dockerfile=. --frontend-opt target=foo --frontend-opt build-arg:foo=bar
```

`context` and `dockerfile` should point to local directories for build context and Dockerfile location.

##### Building a Dockerfile using [external frontend](https://hub.docker.com/r/tonistiigi/dockerfile/tags/):

```
buildctl build --frontend=gateway.v0 --frontend-opt=source=tonistiigi/dockerfile:v0 --local context=. --local dockerfile=.
buildctl build --frontend gateway.v0 --frontend-opt=source=tonistiigi/dockerfile:v0 --frontend-opt=context=git://github.com/moby/moby --frontend-opt build-arg:APT_MIRROR=cdn-fastly.deb.debian.org
````

##### Exporting resulting image to containerd

The containerd worker needs to be used

```
buildctl build ... --exporter=image --exporter-opt name=docker.io/username/image
ctr --namespace=buildkit images ls
```

##### Push resulting image to registry

```
buildctl build ... --exporter=image --exporter-opt name=docker.io/username/image --exporter-opt push=true
```

If credentials are required, `buildctl` will attempt to read Docker configuration file.


##### Exporting build result back to client

```
buildctl build ... --exporter=local --exporter-opt output=path/to/output-dir
```

##### Exporting build result to Docker

```
# exported tarball is also compatible with OCI spec
buildctl build ... --exporter=docker --exporter-opt name=myimage | docker load
```

##### Exporting OCI Image Format tarball to client

```
buildctl build ... --exporter=oci --exporter-opt output=path/to/output.tar
buildctl build ... --exporter=oci > output.tar
```

#### View build cache

```
buildctl du -v
```

#### Running containerized buildkit

buildkit can be also used by running the `buildd` daemon inside a Docker container and accessing it remotely. The client tool `buildctl` is also available for Mac and Windows.

To run daemon in a container:

```
docker run -d --privileged -p 1234:1234 tonistiigi/buildkit --addr tcp://0.0.0.0:1234 --oci-worker=true --containerd-worker=false
export BUILDKIT_HOST=tcp://0.0.0.0:1234
buildctl build --help
```

The `tonistiigi/buildkit` image can be built locally using the Dockerfile in `./hack/dockerfiles/test.Dockerfile`.

#### Supported runc version

During development buildkit is tested with the version of runc that is being used by the containerd repository. Please refer to [runc.md](https://github.com/containerd/containerd/blob/v1.0.0/RUNC.md) for more information.


#### Contributing

Running tests:

```bash
make test
```

This runs all unit and integration tests in a containerized environment. Locally, every package can be tested separately with standard Go tools but integration tests are skipped if local user doesn't have enough permissions or worker binaries are not installed.

```
# test a specific package only
make test TESTPKGS=./client

# run a specific test with all worker combinations
make test TESTPKGS=./client TESTFLAGS="--run /TestCallDiskUsage -v" 

# run all integration tests with a specific worker
# supported workers are oci and containerd
make test TESTPKGS=./client TESTFLAGS="--run //worker=containerd -v" 
```

Updating vendored dependencies:

```bash
# update vendor.conf
make vendor
```

Validating your updates before submission:

```bash
make validate-all
```

#### Documents
- https://github.com/moby/moby/issues/32925: Original proposal
- ['docs/roadmap.md'](./docs/roadmap.md): roadmap (tentative)
- ['docs/misc'](./docs/misc): miscellaneous unformatted documents
- https://drive.google.com/drive/folders/1KpNIqmiAGU1UAddscscZFMqlpvLqcQYM?usp=sharing : BoF notes
