# Rootless mode

Rootless mode allows running BuildKit daemon as a non-root user.

## Known limitations
* Using the `overlayfs` snapshotter requires kernel >= 5.11 or Ubuntu kernel.
  On kernel >= 4.18, the `fuse-overlayfs` snapshotter is used instead of `overlayfs`.
  On kernel < 4.18, the `native` snapshotter is used.
* Network mode is always set to `network.host`.

## Running BuildKit in Rootless mode (OCI worker)

[RootlessKit](https://github.com/rootless-containers/rootlesskit/) needs to be installed.

```bash
rootlesskit buildkitd
```

```bash
buildctl --addr unix:///run/user/$UID/buildkit/buildkitd.sock build ...
```

> [!TIP]
> To isolate BuildKit daemon's network namespace from the host (recommended):
> ```bash
> rootlesskit --net=slirp4netns --copy-up=/etc --disable-host-loopback buildkitd
> ```

## Running BuildKit in Rootless mode (containerd worker)

[RootlessKit](https://github.com/rootless-containers/rootlesskit/) needs to be installed.

Run containerd in rootless mode using rootlesskit following [containerd's document](https://github.com/containerd/containerd/blob/main/docs/rootless.md).

```bash
containerd-rootless.sh

CONTAINERD_NAMESPACE=default containerd-rootless-setuptool.sh install-buildkit-containerd
```

<details>
<summary>Advanced guide</summary>

<p>


Alternatively, you can specify the full command line flags as follows:
```bash
containerd-rootless.sh --config /path/to/config.toml

containerd-rootless-setuptool.sh nsenter -- buildkitd --oci-worker=false --containerd-worker=true
```

</p>

</details>

## Containerized deployment

### Kubernetes
See [`../examples/kubernetes`](../examples/kubernetes).

### Docker

```bash
docker run \
  --name buildkitd \
  -d \
  --security-opt seccomp=unconfined \
  --security-opt apparmor=unconfined \
  --security-opt systempaths=unconfined \
  moby/buildkit:rootless

buildctl --addr docker-container://buildkitd build ...
```

> [!TIP]
> If you don't mind using `--privileged` (almost safe for rootless), the `docker run` flags can be shorten as follows:
>
> ```bash
> docker run --name buildkitd -d --privileged moby/buildkit:rootless
> ```

Justification of the `--security-opt` flags:

* `seccomp=unconfined`: For allowing several syscalls such as `unshare` (used by runc) and `mount` (used by snapshotters, etc).

* `apparmor=unconfined`: For allowing mounting filesystems, etc.
  This flag is not needed when the host operating system does not use AppArmor.

* `systempaths=unconfined`: For disabling the masks for the `/proc` mount in the container, so that each of `ExecOp`
  (corresponds to a `RUN` instruction in Dockerfile) can have a dedicated `/proc` filesystem.
  `systempaths=unconfined` potentially allows reading and writing dangerous kernel files from a container, but it is safe when you are running `buildkitd` as non-root.

> [!TIP]
> Instead of `--security-opt systempaths=unconfined`, `buildkitd` can be also executed with `--oci-worker-no-process-sandbox` (flag of `buildkitd`, not `docker`)
> to avoid creating a new PID namespace and mounting a new `/proc` for it.
>
> Using `--oci-worker-no-process-sandbox` is discouraged, as it cannot terminate processes that did not exit during an `ExecOp`.
> Also, `--oci-worker-no-process-sandbox` allows `ExecOp` containers to `kill` (and potentially `ptrace` depending on the seccomp configuration) an arbitrary process in the BuildKit daemon container.
>
> Despite these caveats, the [Kubernetes examples](../examples/kubernetes) uses `--oci-worker-no-process-sandbox`, as Kubernetes lacks the equivalent of `systempaths=unconfined`.
> (`securityContext.procMount=Unmasked` is similar, but different in the sense that it depends on `hostUsers: false`)

### Change UID/GID

The `moby/buildkit:rootless` image has the following UID/GID configuration:

Actual ID (shown in the host and the BuildKit daemon container)| Mapped ID (shown in build executor containers)
----------|----------
1000      | 0
100000    | 1
...       | ...
165535    | 65536

```console
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
```bash
vi Dockerfile
make images
docker run ... moby/buildkit:local-rootless ...
```

## Troubleshooting

### Error related to `overlayfs`
Try running `buildkitd` with `--oci-worker-snapshotter=fuse-overlayfs`:

```console
$ rootlesskit buildkitd --oci-worker-snapshotter=fuse-overlayfs
```

### Error related to `fuse-overlayfs`
Run `docker run`  with `--device /dev/fuse`.

Also try running `buildkitd` with `--oci-worker-snapshotter=native`:

```console
$ rootlesskit buildkitd --oci-worker-snapshotter=native
```

### Error related to `newuidmap` or `/etc/subuid`
See https://rootlesscontaine.rs/getting-started/common/subuid/

### Error `Options:[rbind ro]}]: operation not permitted`
Make sure to mount an `emptyDir` volume on `/home/user/.local/share/buildkit` .

### Error `fork/exec /proc/self/exe: no space left on device` with `level=warning msg="/proc/sys/user/max_user_namespaces needs to be set to non-zero."`
Run `sysctl -w user.max_user_namespaces=N` (N=positive integer, like 63359) on the host nodes.

See [`../examples/kubernetes/sysctl-userns.privileged.yaml`](../examples/kubernetes/sysctl-userns.privileged.yaml).

### Error `fork/exec /proc/self/exe: permission denied` with `This error might have happened because /proc/sys/kernel/apparmor_restrict_unprivileged_userns is set to 1`
Add `kernel.apparmor_restrict_unprivileged_userns=0` to `/etc/sysctl.conf` (or `/etc/sysctl.d`) and run `sudo sysctl -p`.

### Error `mount proc:/proc (via /proc/self/fd/6), flags: 0xe: operation not permitted`
This error is known to happen when BuildKit is executed in a container without the `--security-opt systempaths=unconfined` flag.
Make sure to specify it (See [above](#docker)).

## Distribution-specific hint
Using Ubuntu kernel is recommended.

### Ubuntu, 24.04 or later
Add `kernel.apparmor_restrict_unprivileged_userns=0` to `/etc/sysctl.conf` (or `/etc/sysctl.d`) and run `sudo sysctl -p`.

### Container-Optimized OS from Google
Make sure to have an `emptyDir` volume below:
```yaml
spec:
  containers:
    - name: buildkitd
      volumeMounts:
        # Dockerfile has `VOLUME /home/user/.local/share/buildkit` by default too,
        # but the default VOLUME does not work with rootless on Google's Container-Optimized OS
        # as it is mounted with `nosuid,nodev`.
        # https://github.com/moby/buildkit/issues/879#issuecomment-1240347038
        - mountPath: /home/user/.local/share/buildkit
          name: buildkitd
  volumes:
    - name: buildkitd
      emptyDir: {}
```

See also the [example manifests](#Kubernetes).

### Bottlerocket OS

Needs to set the max user namespaces to a positive integer, through the [API settings](https://github.com/bottlerocket-os/bottlerocket#kernel-settings):

```toml
[settings.kernel.sysctl]
"user.max_user_namespaces" = "16384"
```

See [`../examples/eksctl/bottlerocket.yaml`](../examples/eksctl/bottlerocket.yaml) for an example to configure a Node Group in EKS.

<details>
<summary>Old distributions</summary>

<p>

### Debian GNU/Linux 10
Add `kernel.unprivileged_userns_clone=1` to `/etc/sysctl.conf` (or `/etc/sysctl.d`) and run `sudo sysctl -p`.
This step is not needed for Debian GNU/Linux 11 and later.

### RHEL/CentOS 7
Add `user.max_user_namespaces=28633` to `/etc/sysctl.conf` (or `/etc/sysctl.d`) and run `sudo sysctl -p`.
This step is not needed for RHEL/CentOS 8 and later.

### Fedora, before kernel 5.13
You may have to disable SELinux, or run BuildKit with `--oci-worker-snapshotter=fuse-overlayfs`.

</p>
</details>
