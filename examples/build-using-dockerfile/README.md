# `build-using-dockerfile` example

:information_source: [BuildKit has been integrated to `docker build` since Docker 18.06.](https://docs.docker.com/develop/develop-images/build_enhancements/)
The `build-using-dockerfile` CLI is just provided as an example for writing a BuildKit client application.

For people familiar with `docker build` command, `build-using-dockerfile` is provided as an example for building Dockerfiles with BuildKit using a syntax similar to `docker build`.

```bash
go get .

build-using-dockerfile -t myimage /path/to/dir

# build-using-dockerfile will automatically load the resulting image to Docker
docker inspect myimage
```
