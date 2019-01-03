# Rootless mode (Experimental)

Requirements:
- runc `a00bf0190895aa465a5fbed0268888e2c8ddfe85` (Oct 15, 2018) or later
- Some distros such as Debian (excluding Ubuntu) and Arch Linux require `sudo sh -c "echo 1 > /proc/sys/kernel/unprivileged_userns_clone"`.
- RHEL/CentOS 7 requires `sudo sh -c "echo 28633 > /proc/sys/user/max_user_namespaces"`. You may also need `sudo grubby --args="namespace.unpriv_enable=1 user_namespace.enable=1" --update-kernel="$(grubby --default-kernel)"`.
- `newuidmap` and `newgidmap` need to be installed on the host. These commands are provided by the `uidmap` package. For RHEL/CentOS 7, RPM is not officially provided but available at https://copr.fedorainfracloud.org/coprs/vbatts/shadow-utils-newxidmap/ .
- `/etc/subuid` and `/etc/subgid` should contain >= 65536 sub-IDs. e.g. `penguin:231072:65536`.


## Set up

Setting up rootless mode also requires some bothersome steps as follows, but you can also use [`rootlesskit`](https://github.com/rootless-containers/rootlesskit) for automating these steps.

### Terminal 1:

```
$ unshare -U -m
unshared$ echo $$ > /tmp/pid
```

Unsharing mountns (and userns) is required for mounting filesystems without real root privileges.

### Terminal 2:

```
$ id -u
1001
$ grep $(whoami) /etc/subuid
penguin:231072:65536
$ grep $(whoami) /etc/subgid
penguin:231072:65536
$ newuidmap $(cat /tmp/pid) 0 1001 1 1 231072 65536
$ newgidmap $(cat /tmp/pid) 0 1001 1 1 231072 65536
```

### Terminal 1:

```
unshared# buildkitd
```

* The data dir will be set to `/home/penguin/.local/share/buildkit`
* The address will be set to `unix:///run/user/1001/buildkit/buildkitd.sock`
* `overlayfs` snapshotter is not supported except Ubuntu-flavored kernel: http://kernel.ubuntu.com/git/ubuntu/ubuntu-artful.git/commit/fs/overlayfs?h=Ubuntu-4.13.0-25.29&id=0a414bdc3d01f3b61ed86cfe3ce8b63a9240eba7
* containerd worker is not supported ( pending PR: https://github.com/containerd/containerd/pull/2006 )
* Network namespace is not used at the moment.
* Cgroups is disabled.

### Terminal 2:

```
$ go get ./examples/build-using-dockerfile
$ build-using-dockerfile --buildkit-addr unix:///run/user/1001/buildkit/buildkitd.sock -t foo /path/to/somewhere
```

## Set up (using a container)

Docker image is available as [`moby/buildkit:rootless`](https://hub.docker.com/r/moby/buildkit/tags/).

```
$ docker run --name buildkitd -d --privileged -p 1234:1234  moby/buildkit:rootless --addr tcp://0.0.0.0:1234
```

```
$ go get ./examples/build-using-dockerfile
$ build-using-dockerfile --buildkit-addr tcp://127.0.0.1:1234 -t foo /path/to/somewhere
```

### Security consideration

Although `moby/buildkit:rootless` executes the BuildKit daemon as a normal user, `docker run` still requires `--privileged`.
This is to allow build executor containers to mount `/proc`, by providing "unmasked" `/proc` to the BuildKit daemon container.

See [`docker/cli#1347`](https://github.com/docker/cli/pull/1347) for the ongoing work to remove this requirement.
See also [Disabling process sandbox](#disabling-process-sandbox).

#### UID/GID

The `moby/buildkit:rootless` image has the following UID/GID configuration:

Actual ID (shown in the host and the BuildKit daemon container)| Mapped ID (shown in build executor containers)
----------|----------
1000      | 0
100000    | 1
...       | ...
165535    | 65536

```
$ docker exec buildkitd id
uid=1000(user) gid=1000(user)
$ docker exec buildkitd ps aux
PID   USER     TIME   COMMAND
    1 user       0:00 rootlesskit buildkitd --addr tcp://0.0.0.0:1234
   13 user       0:00 /proc/self/exe buildkitd --addr tcp://0.0.0.0:1234
   21 user       0:00 buildkitd --addr tcp://0.0.0.0:1234
   29 user       0:00 ps aux
$ docker exec cat /etc/subuid
user:100000:65536
```

To change the UID/GID configuration, you need to modify and build the BuildKit image manually.
```
$ vi hack/dockerfiles/test.Dockerfile
$ docker build -t buildkit-rootless-custom --target rootless -f hack/dockerfiles/test.Dockerfile .
```

#### Disabling process sandbox

By passing `--oci-worker-no-process-sandbox` to the `buildkitd` arguments, BuildKit can be executed in a container without `--privileged`.
However, you still need to pass `--security-opt seccomp=unconfined --security-opt apparmor=unconfined` to `docker run`.

```
$ docker run --name buildkitd -d --security-opt seccomp=unconfined --security-opt apparmor=unconfined -p 1234:1234 moby/buildkit:rootless --addr tcp://0.0.0.0:1234 --oci-worker-no-process-sandbox
```

Note that `--oci-worker-no-process-sandbox` allows build executor containers to `kill` (and potentially `ptrace` depending on the seccomp configuration) an arbitrary process in the BuildKit daemon container.


## Set up (using Kubernetes)

### With `securityContext`

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  labels:
    app: buildkitd
  name: buildkitd
spec:
  selector:
    matchLabels:
      app: buildkitd
  template:
    metadata:
      labels:
        app: buildkitd
    spec:
      containers:
      - image: moby/buildkit:rootless
        args:
        - --addr
        - tcp://0.0.0.0:1234
        name: buildkitd
        ports:
        - containerPort: 1234
        securityContext:
          privileged: true
```

This configuration requires privileged containers to be enabled.

If you are using Kubernetes v1.12+ with either Docker v18.06+, containerd v1.2+, or CRI-O v1.12+ as the CRI runtime, you can replace `privileged: true` with `procMount: Unmasked`.

### Without `securityContext` but with `--oci-worker-no-process-sandbox`

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  labels:
    app: buildkitd
  name: buildkitd
spec:
  selector:
    matchLabels:
      app: buildkitd
  template:
    metadata:
      labels:
        app: buildkitd
    spec:
      containers:
      - image: moby/buildkit:rootless
        args:
        - --addr
        - tcp://0.0.0.0:1234
        - --oci-worker-no-process-sandbox
        name: buildkitd
        ports:
        - containerPort: 1234
```

See [Disabling process sandbox](#disabling-process-sandbox) for security notice.
