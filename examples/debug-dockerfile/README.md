# BuildKit frontend for debugging failed Dockerfile

For example, we debug the following Dockerfile:

```Dockerfile
FROM ghcr.io/stargz-containers/ubuntu:20.04-org
RUN echo hello > /hello
RUN cat /hello

# This will fail
RUN cat /non-existing-file
```

The following command launches an interactive shell on a failed build.
Shell is launced on the failed step, followed by `========= launching shell on a failed step =========`.

```console
# buildctl build --frontend=gateway.v0 --opt source=ghcr.io/ktock/dockerfile-debug-frontend:1 \
           --interactive --progress=plain \
           --local context=/tmp/debug --local dockerfile=/tmp/debug \
           --output=type=oci,dest=/tmp/img.tar
#1 resolve image config for ghcr.io/ktock/dockerfile-debug-frontend:1
#1 ...

#2 [auth] ktock/dockerfile-debug-frontend:pull token for ghcr.io
#2 DONE 0.0s

#1 resolve image config for ghcr.io/ktock/dockerfile-debug-frontend:1
#1 DONE 2.1s

#3 docker-image://ghcr.io/ktock/dockerfile-debug-frontend:1@sha256:46a376c05a4ce949c7036d9e0bd217b2c20f5d7ee158a1ee37d76ae7be1696c0
#3 resolve ghcr.io/ktock/dockerfile-debug-frontend:1@sha256:46a376c05a4ce949c7036d9e0bd217b2c20f5d7ee158a1ee37d76ae7be1696c0 0.0s done
#3 sha256:293800bc728a09b1fc1b3515cf03a2423cf600eae57234ff2cfe18d8a1fd92a9 0B / 10.64MB 0.2s
#3 sha256:293800bc728a09b1fc1b3515cf03a2423cf600eae57234ff2cfe18d8a1fd92a9 4.19MB / 10.64MB 0.6s
#3 extracting sha256:293800bc728a09b1fc1b3515cf03a2423cf600eae57234ff2cfe18d8a1fd92a9
#3 extracting sha256:293800bc728a09b1fc1b3515cf03a2423cf600eae57234ff2cfe18d8a1fd92a9 0.2s done
#3 DONE 1.0s

#4 [internal] load .dockerignore
#4 transferring context: 2B done
#4 DONE 0.0s

#5 [internal] load build definition from Dockerfile
#5 transferring dockerfile: 171B done
#5 DONE 0.1s

#6 [internal] load metadata for ghcr.io/stargz-containers/ubuntu:20.04-org
#6 ...

#7 [auth] stargz-containers/ubuntu:pull token for ghcr.io
#7 DONE 0.0s

#6 [internal] load metadata for ghcr.io/stargz-containers/ubuntu:20.04-org
#6 DONE 2.1s

#8 [1/4] FROM ghcr.io/stargz-containers/ubuntu:20.04-org@sha256:adf73ca014822ad8237623d388cedf4d5346aa72c270c5acc01431cc93e18e2d
#8 resolve ghcr.io/stargz-containers/ubuntu:20.04-org@sha256:adf73ca014822ad8237623d388cedf4d5346aa72c270c5acc01431cc93e18e2d 0.0s done
#8 DONE 0.1s

#8 [1/4] FROM ghcr.io/stargz-containers/ubuntu:20.04-org@sha256:adf73ca014822ad8237623d388cedf4d5346aa72c270c5acc01431cc93e18e2d
#8 sha256:5e9250ddb7d0fa6d13302c7c3e6a0aa40390e42424caed1e5289077ee4054709 0B / 187B 0.2s
#8 sha256:57671312ef6fdbecf340e5fed0fb0863350cd806c92b1fdd7978adbd02afc5c3 0B / 851B 0.2s
#8 sha256:345e3491a907bb7c6f1bdddcf4a94284b8b6ddd77eb7d93f09432b17b20f2bbe 0B / 28.54MB 0.2s
#8 sha256:345e3491a907bb7c6f1bdddcf4a94284b8b6ddd77eb7d93f09432b17b20f2bbe 10.49MB / 28.54MB 0.8s
#8 sha256:345e3491a907bb7c6f1bdddcf4a94284b8b6ddd77eb7d93f09432b17b20f2bbe 18.87MB / 28.54MB 0.9s
#8 sha256:345e3491a907bb7c6f1bdddcf4a94284b8b6ddd77eb7d93f09432b17b20f2bbe 26.21MB / 28.54MB 1.1s
#8 extracting sha256:345e3491a907bb7c6f1bdddcf4a94284b8b6ddd77eb7d93f09432b17b20f2bbe
#8 extracting sha256:345e3491a907bb7c6f1bdddcf4a94284b8b6ddd77eb7d93f09432b17b20f2bbe 0.8s done
#8 extracting sha256:57671312ef6fdbecf340e5fed0fb0863350cd806c92b1fdd7978adbd02afc5c3 0.0s done
#8 extracting sha256:5e9250ddb7d0fa6d13302c7c3e6a0aa40390e42424caed1e5289077ee4054709 0.0s done
#8 DONE 2.1s

#9 [2/4] RUN echo hello > /hello
#9 DONE 0.2s

#10 [3/4] RUN cat /hello
#0 0.131 hello
#10 DONE 0.2s

#11 [4/4] RUN cat /non-existing-file

========= launching shell on a failed step =========
#11 0.102 cat: /non-existing-file: No such file or directory
#11 ERROR: process "/bin/sh -c cat /non-existing-file" did not complete successfully: exit code: 1
# 
# ls /
bin   dev  hello  lib	 lib64	 media	opt   root  sbin  sys  usr
boot  etc  home   lib32  libx32  mnt	proc  run   srv   tmp  var
# cat /hello
hello
# cat /non-existing-file
cat: /non-existing-file: No such file or directory
# exit

process execution failed: exit code: 1

====================================================
------
 > [4/4] RUN cat /non-existing-file:
#11 0.102 cat: /non-existing-file: No such file or directory
------
Dockerfile:6
--------------------
   4 |     
   5 |     # This will fail
   6 | >>> RUN cat /non-existing-file
   7 |     
--------------------
error: failed to solve: process "/bin/sh -c cat /non-existing-file" did not complete successfully: exit code: 1
```

## Example of building frontend image

```console
$ mkdir -p /tmp/ctx && cat <<EOF > /tmp/ctx/Dockerfile
FROM golang:1.18-alpine AS build-dockerfile
COPY . /build
RUN apk add git
RUN cd /build && go build -o /out/dockerfile-debug-frontend -tags "netgo static_build osusergo" ./examples/debug-dockerfile/

FROM scratch
COPY --from=build-dockerfile /out/dockerfile-debug-frontend /
ENTRYPOINT ["/dockerfile-debug-frontend"]
EOF
$ docker build -t ghcr.io/ktock/dockerfile-debug-frontend:1 -f /tmp/ctx/Dockerfile $GOPATH/src/github.com/moby/buildkit
```
