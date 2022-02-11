# Examples

## Kubernetes manifests
 
- [Kubernetes](kubernetes.md): Kubernetes manifests (`Pod`, `Deployment`, `StatefulSet`, `Job`)

## CLI examples

- [`buildctl` daemonless](buildctl-daemonless.md): buildctl without daemon
- [Build using a Dockerfile](build-using-dockerfile.md): an example BuildKit client with `docker build`-style CLI

## LLB examples

For understanding the basics of LLB, `buildkit*` directory contains scripts that
define how to build different configurations of BuildKit itself and its
dependencies using the `client` package. Running one of these scripts generates
a protobuf definition of a build graph. Note that the script itself does not
execute any steps of the build.

You can use `buildctl debug dump-llb` to see what data is in this definition.
Add `--dot` to generate dot layout.

```shell
go run examples/buildkit0/buildkit.go \
    | buildctl debug dump-llb \
    | jq .
```

To start building use `buildctl build` command. The example script accepts
`--with-containerd` flag to choose if containerd binaries and support should be
included in the end result as well.

```shell
go run examples/buildkit0/buildkit.go \
    | buildctl build
```

`buildctl build` will show interactive progress bar by default while the build
job is running. If the path to the trace file is specified, the trace file
generated will contain all information about the timing of the individual steps
and logs.

Different versions of the example scripts show different ways of describing the
build definition for this project to show the capabilities of the library. New
versions have been added when new features have become available.

- [`buildkit0`](https://github.com/moby/buildkit/tree/master/examples/buildkit0) - uses only exec operations, defines a full stage per component.
- [`buildkit1`](https://github.com/moby/buildkit/tree/master/examples/buildkit1) - cloning git repositories has been separated for extra concurrency.
- [`buildkit2`](https://github.com/moby/buildkit/tree/master/examples/buildkit2) - uses git sources directly instead of running `git clone`, allowing better performance and much safer caching.
- [`buildkit3`](https://github.com/moby/buildkit/tree/master/examples/buildkit3) - allows using local source files for separate components eg. `./buildkit3 --runc=local | buildctl build --local runc-src=some/local/path`
- [`dockerfile2llb`](https://github.com/moby/buildkit/tree/master/examples/dockerfile2llb) - can be used to convert a Dockerfile to LLB for debugging purposes
- [`nested-llb`](https://github.com/moby/buildkit/tree/master/examples/nested-llb) - shows how to use nested invocation to generate LLB
- [`gobuild`](https://github.com/moby/buildkit/tree/master/examples/gobuild) - shows how to use nested invocation to generate LLB for Go package internal dependencies
