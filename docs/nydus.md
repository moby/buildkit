# Nydus Integration With Buildkit (Experimental)

## Nydus Image Overview

Nydus image is a container accelerated image format provided by the [Dragonfly image-service project](https://github.com/dragonflyoss/image-service), which offer the ability to pull image data on demand, without waiting for the entire image pull to complete and then start the container. It has been put in production usage and shown vast improvements over the old OCI image format in terms of container launching speed, image space and network bandwidth efficiency, as well as data integrity. The Nydus image can also serve as an example and reference implementation for the on-going OCI image spec v2 discussion.

The [benchmarking result](https://github.com/dragonflyoss/image-service/raw/master/misc/perf.jpg) shows the performance improvement compared with the OCI image for the container cold startup elapsed time on containerd. As the OCI image size increases, the container startup time of using Nydus image remains very short.

## Key Features On Nydus

Nydus designs and implements a user space filesystem on top of a special designed container image format that improves at all the above mentioned OCI image spec defects. Key features include:

- Container images are downloaded on demand;
- Chunk level data duplication with configurable chunk size;
- Flatten image metadata and data to remove all intermediate layers;
- Only usable image data is saved when building a container image;
- Only usable image data is downloaded when running a container;
- End-to-end image data integrity;
- Compatible with the OCI artifacts spec and distribution spec;
- Integrated with existing CNCF project dragonfly to support image distribution in large clusters;
- Except for the OCI distribution spec, other image storage backends can be supported as well;

## Nydus Image In A Nutshell

The Nydus image format follows the OCI v1 image spec for storing and distributing image in the current OCI ecosystem. See the [Nydus Image Example](https://github.com/dragonflyoss/image-service/blob/master/contrib/nydusify/examples/manifest/manifest.json), the image manifest indexes Nydus' RAFS(Registry Acceleration File System) filesystem layer, which is a strongly verified filesystem with separate metadata (bootstrap) and data (blob). The bootstrap contains meta information about files and directories of all layers for an image and related checksum information, while the blob is the data of each layer, which is composed of many data chunks, each chunk may be shared by a layer or with other layers.

With two annotation hints on the layer, [Nydus Snapshotter](https://github.com/dragonflyoss/image-service/tree/master/contrib/nydus-snapshotter) can let containerd download only the bootstrap layer of the image, but not the blob layer, so that data can be loaded on demand. At the same time, the dependency relationship between image manifest and layer is compatible with the existing image dependency tree and garbage collection mechanism of containerd and registry.

The Nydus image currently requires the [Nydus Snapshotter](https://github.com/dragonflyoss/image-service/tree/master/contrib/nydus-snapshotter) to run. For better compatibility, Nydus also supports merging the Nydus manifest with the OCI manifest into a [manifest index](https://github.com/dragonflyoss/image-service/blob/master/contrib/nydusify/examples/manifest/index.json), and adding an OS Feature field to differentiate between them. This design allows Snapshotter that does not support Nydus to give preference to the OCI image to run, and maintain compatibility without changing anything.

See more about Nydus image format on the [design](https://github.com/dragonflyoss/image-service/blob/master/docs/nydus-design.md) doc.

## Implement Nydus Exporter In Buildkit

Nydus has provided a conversion tool [Nydusify](https://github.com/dragonflyoss/image-service/blob/master/docs/nydusify.md) for converting OCI image to Nydus image, which assumes that the OCI image is already available in the registry, but a better way would be to build the Nydus images directly from the build system instead of using the conversion tool, which would increase the speed of the image export, so we experimentally integrated the Nydus exporter in Buildkit.

Nydusify provides a package to do the actual image conversion. We use the same package to implement the buildkit nydus exporter. The exporter mount all layers and call the package to build to a Nydus image and push it to the remote registry. The current implementation relies on the Nydus [builder](https://github.com/dragonflyoss/image-service/blob/master/docs/nydus-image.md) (written in rust), which is used to build a layer to a RAFS layer, and in the future we can explore simpler, less dependent integrations.

## Known Limitations

- Exporter currently relies on the Nydus [builder](https://github.com/dragonflyoss/image-service/blob/master/docs/nydus-image.md) as the core build tool;
- Currently only supports linux/amd64 platform image export;

# Nydus Exporter Usage

This section describes how to use Buildkit to export Nydus image. Nydus exporter depends on an external nydus-image binary. It can be obtained from the [Nydus Releases](https://github.com/dragonflyoss/image-service/releases) page, named `nydus-image` in tgz. We need put the binary into the directories named by the PATH environment variable.

## Export with buildctl

```shell
$ buildctl build --frontend=dockerfile.v0 \
  --local context=/tmp/hello \
  --local dockerfile=/tmp/hello \
  --output type=nydus,name=localhost:5000/hello
```

Keys supported by Nydus exporter:

- name=[value]: Nydus image reference
- registry.insecure=true: push to insecure HTTP registry
- oci-mediatypes=true: use OCI mediatypes in Nydus image manifest instead of Docker's
- merge-manifest=true: merge into manifest index if remote manifest exists

## Run container with Nydus image

After building with buildkit, the image should be pushed to remote registry, now we can run a container with containerd from a Nydus image, [here](https://github.com/dragonflyoss/image-service/blob/master/docs/containerd-env-setup.md) is a setup tutorial.
