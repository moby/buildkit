# CDI

[CDI (Container Device Interface)](https://github.com/cncf-tags/container-device-interface/blob/main/SPEC.md)
provides a standard mechanism for device vendors to describe what is required
to provide access to a specific resource such as a GPU beyond a simple device
name.

Since BuildKit 0.20.0, you can access devices using the CDI interface. This
allows you to use devices like GPUs in your builds.

## Usage

To use CDI with BuildKit, you need to create the [CDI configuration file](https://github.com/cncf-tags/container-device-interface/blob/main/SPEC.md#cdi-json-specification)
for your device (either JSON or YAML) and put it in one of the following
locations:
* `/etc/cdi`
* `/var/run/cdi`
* `/etc/buildkit/cdi`

> [!NOTE]
> Location can be changed by setting the `specDirs` option in the `cdi` section
> of the [`buildkitd.toml` configuration file](buildkitd.toml.md).

Let's create a simple CDI configuration file that injects an environment
variable into the build environment and write it to `/etc/cdi/foo.yml`:

```yaml
cdiVersion: "0.6.0"
kind: "vendor1.com/device"
devices:
- name: foo
  containerEdits:
    env:
    - FOO=injected
```

Start BuildKit and check the list of available devices for the default worker:

```bash
buildctl debug workers -v

Platforms:      linux/amd64,linux/amd64/v2,linux/amd64/v3,linux/arm64,linux/riscv64,linux/ppc64le,linux/s390x,linux/386,linux/arm/v7,linux/arm/v6
BuildKit:       github.com/moby/buildkit v0.19.0-rc3-72-gb89ee491b.m b89ee491bc1c40b8533f098dab18e414c8b70885.m
Labels:
        org.mobyproject.buildkit.worker.executor:               oci
        org.mobyproject.buildkit.worker.hostname:               cc39352c87dd
        org.mobyproject.buildkit.worker.network:                host
        org.mobyproject.buildkit.worker.oci.process-mode:       sandbox
        org.mobyproject.buildkit.worker.selinux.enabled:        false
        org.mobyproject.buildkit.worker.snapshotter:            overlayfs
Devices:
        Name:           vendor1.com/device=foo
        AutoAllow:      true

GC Policy rule#0:
        All:                    false
        Filters:                type==source.local,type==exec.cachemount,type==source.git.checkout
        Keep duration:          48h0m0s
        Maximum used space:     512MB
GC Policy rule#1:
        All:                    false
        Keep duration:          1440h0m0s
        Reserved space:         10GB
        Minimum free space:     202GB
        Maximum used space:     100GB
GC Policy rule#2:
        All:                    false
        Reserved space:         10GB
        Minimum free space:     202GB
        Maximum used space:     100GB
GC Policy rule#3:
        All:                    true
        Reserved space:         10GB
        Minimum free space:     202GB
        Maximum used space:     100GB
```

Now let's create a Dockerfile to use this device:

```dockerfile
FROM busybox
RUN --device=vendor1.com/device=foo \
  env|grep FOO
```

And check if the environment variable is injected during build:

```bash
buildctl build --frontend=dockerfile.v0 --no-cache --local context=. --local dockerfile=.
#1 [internal] load build definition from Dockerfile
#1 transferring dockerfile: 99B done
#1 DONE 0.0s

#2 [internal] load metadata for docker.io/library/busybox:latest
#2 ...

#3 [auth] library/busybox:pull token for registry-1.docker.io
#3 DONE 0.0s

#2 [internal] load metadata for docker.io/library/busybox:latest
#2 DONE 0.8s

#4 [internal] load .dockerignore
#4 transferring context: 2B done
#4 DONE 0.0s

#5 [1/2] FROM docker.io/library/busybox:latest@sha256:a5d0ce49aa801d475da48f8cb163c354ab95cab073cd3c138bd458fc8257fbf1
#5 resolve docker.io/library/busybox:latest@sha256:a5d0ce49aa801d475da48f8cb163c354ab95cab073cd3c138bd458fc8257fbf1 0.0s done
#5 CACHED

#6 [2/2] RUN --device=vendor1.com/device=foo   env|grep FOO
#6 0.062 FOO=injected
#6 DONE 0.1s
```
