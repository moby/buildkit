# Rootless mode (Experimental)

Rootless mode allows running BuildKit daemon as a non-root user.

## Distribution-specific hint
Using Ubuntu kernel is recommended.

### Ubuntu
* No preparation is needed.
* `overlayfs` snapshotter is enabled by default ([Ubuntu-specific kernel patch](https://kernel.ubuntu.com/git/ubuntu/ubuntu-bionic.git/commit/fs/overlayfs?id=3b7da90f28fe1ed4b79ef2d994c81efbc58f1144)).

### Debian GNU/Linux
* Add `kernel.unprivileged_userns_clone=1` to `/etc/sysctl.conf` (or `/etc/sysctl.d`) and run `sudo sysctl -p`
* To use `overlayfs` snapshotter (recommended), run `sudo modprobe overlay permit_mounts_in_userns=1` ([Debian-specific kernel patch, introduced in Debian 10](https://salsa.debian.org/kernel-team/linux/blob/283390e7feb21b47779b48e0c8eb0cc409d2c815/debian/patches/debian/overlayfs-permit-mounts-in-userns.patch)). Put the configuration to `/etc/modprobe.d` for persistence.

### Arch Linux
* Add `kernel.unprivileged_userns_clone=1` to `/etc/sysctl.conf` (or `/etc/sysctl.d`) and run `sudo sysctl -p`

### Fedora 31 and later
* Run `sudo grubby --update-kernel=ALL --args="systemd.unified_cgroup_hierarchy=0"` and reboot.

### Fedora 30
* No preparation is needed

### RHEL/CentOS 8
* No preparation is needed

### RHEL/CentOS 7
* Add `user.max_user_namespaces=28633` to `/etc/sysctl.conf` (or `/etc/sysctl.d`) and run `sudo sysctl -p`
* Old releases (<= 7.6) require [extra configuration steps](https://github.com/moby/moby/pull/40076).

### Container-Optimized OS from Google
* :warning: Currently unsupported. See [#879](https://github.com/moby/buildkit/issues/879).

## Known limitations
* No support for `overlayfs` snapshotter, except on Ubuntu and Debian kernels. We are planning to support `fuse-overlayfs` snapshotter instead for other kernels.
* Network mode is always set to `network.host`.
* No support for `containerd` worker

## Running BuildKit in Rootless mode

[RootlessKit](https://github.com/rootless-containers/rootlesskit/) needs to be installed.

```console
$ rootlesskit buildkitd
```

```console
$ buildctl --addr unix:///run/user/$UID/buildkit/buildkitd.sock build ...
```

## Containerized deployment

### Kubernetes
See [`../examples/kubernetes`](../examples/kubernetes).

### Docker

```console
$ docker run --name buildkitd -d --security-opt seccomp=unconfined --security-opt apparmor=unconfined moby/buildkit:rootless --oci-worker-no-process-sandbox
$ buildctl --addr docker-container://buildkitd build ...
```
#### About `--oci-worker-no-process-sandbox`

By adding `--oci-worker-no-process-sandbox` to the `buildkitd` arguments, BuildKit can be executed in a container without adding `--privileged` to `docker run` arguments.
However, you still need to pass `--security-opt seccomp=unconfined --security-opt apparmor=unconfined` to `docker run`.

Note that `--oci-worker-no-process-sandbox` allows build executor containers to `kill` (and potentially `ptrace` depending on the seccomp configuration) an arbitrary process in the BuildKit daemon container.

To allow running rootless `buildkitd` without `--oci-worker-no-process-sandbox`, run `docker run` with `--security-opt systempaths=unconfined`. (For Kubernetes, set `securityContext.procMount` to `Unmasked`.)

The `--security-opt systempaths=unconfined` flag disables the masks for the `/proc` mount in the container and potentially allows reading and writing dangerous kernel files, but it is safe when you are running `buildkitd` as non-root.

### Change UID/GID

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
$ vi Dockerfile
$ make images
$ docker run ... moby/buildkit:local-rootless ...
```

