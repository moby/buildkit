# Building multi-platform images

| :zap: For building multi-platform images with `docker buildx`, see [the `docker buildx` documentation](https://github.com/docker/buildx#building-multi-platform-images). |
|--------------------------------------------------------------------------|



BuildKit provides built-in support for building multi-platform by setting a comma-separated list of
[platform specifiers](https://github.com/containerd/containerd/blob/v1.5.7/platforms/platforms.go#L63) as `platform` option.

```bash
buildctl build \
  --frontend dockerfile.v0 \
  --opt platform=linux/amd64,linux/arm64 \
  --output type=image,name=docker.io/username/image,push=true \
  ...
```

When your build needs to run a binary for architecture that is not supported natively by your host, it gets executed using a QEMU user-mode emulator.
You do not need to set up QEMU manually in most cases.

## Troubleshooting

### Error `exec user process caused: exec format error`

You may face an error like `exec user process caused: exec format error`, mostly when you are using a third-party package of BuildKit that lacks
`buildkit-qemu-*` binaries.

In such a case, you have to download the official binary release of BuildKit from https://github.com/moby/buildkit/releases , and install
the `buildkit-qemu-*` binaries in the release archive into the `$PATH` of the host.

You may also face `exec format error` when the container contains mix of binaries for multiple architectures.

In such a case, you have to register QEMU into `/proc/sys/fs/binfmt_misc` so that the kernel can execute foreign binaries using QEMU.

QEMU is registered into `/proc/sys/fs/binfmt_misc` by default on Docker Desktop.
On other environments, the common way to register QEMU is to use `tonistiigi/binfmt` Docker image.

```bash
docker run --privileged --rm tonistiigi/binfmt --install all
```

See also [`tonistiigi/binfmt` documentation](https://github.com/tonistiigi/binfmt/).
