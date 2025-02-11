# Overlaybd

[Overlaybd](https://github.com/containerd/overlaybd) is a novel layering block-level image format, which is design for container, secure container and applicable to virtual machine. And it is an open-source implementation of paper [DADI: Block-Level Image Service for Agile and Elastic Application Deployment. USENIX ATC'20"](https://www.usenix.org/conference/atc20/presentation/li-huiba).

## Build Overlaybd Images

Before building overlaybd images, ensure that `overlaybd-tcmu` and `overlaybd-snapshotter` are active by referring to the [QUICKSTART](https://github.com/containerd/accelerated-container-image/blob/main/docs/QUICKSTART.md#install) guide.

To use buildkit to build overlaybd images, you should specify `--oci-worker-snapshotter=overlaybd` and `--oci-worker-proxy-snapshotter-path=/run/overlaybd-snapshotter/overlaybd.sock` when start buildkitd:

```bash
buildkitd  --oci-worker-snapshotter=overlaybd  --oci-worker-proxy-snapshotter-path=/run/overlaybd-snapshotter/overlaybd.sock
```
If an overlaybd image is used in the FROM instruction of a Dockerfile, the build will produce a new overlaybd image. It is essential to include `--oci-mediatypes=true` and `--compression=uncompressed` while running buildctl:

```bash
buildctl build ... \
--output type=image,name=<new image name>,push=true,oci-mediatypes=true,compression=uncompressed
```

## Performance

In our test case Dockerfile, we used a 5GB OCI image (and corresponding overlaybd format), wrote some new layers of identical size, and recorded the time cost of image pull (as **pull** in the table below), building all lines in Dockerfile (as **build**), and exporting to image and pushing (as **push**).

OCI:

| **size per layer** | **layers** | **pull** | **build** | **push** | **total**  |
| -------- | ---- | ---- | ----- | ---- | ---- |
| 4GB      | 1    | 105.7| 23.5  | 219.4| 348.6|
| 1GB      | 4    | 88.5 | 34.0  | 123.8| 246.3|
| 256MB    | 10   | 92.1 | 20.7  | 63.6 | 176.4|

Overlaybd:

| **size per layer** | **layers** | **pull** | **build** | **push** | **total**  |
| -------- | ---- | ---- | ----- | ---- | ---- |
| 4GB      | 1    | 0.9  | 21.5  | 166.2| 188.6|
| 1GB      | 4    | 0.9	 | 24.9	 | 72.9 | 98.7 |
| 256MB    | 10   | 0.7  | 18.4  | 48.9 | 68.0 |