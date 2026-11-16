# Host cache mounts

Cache mounts (`RUN --mount=type=cache` in a Dockerfile, `llb.AsPersistentCacheDir`
in LLB) normally live in the snapshotter, inside the BuildKit state directory.
Their contents are lost when that directory is, which is the common case for
builders that are created for a single CI job, and they can't be shared between
BuildKit daemons.

`buildkitd` can serve selected cache mount IDs from a directory on the host
instead. The directory can sit on a persistent disk, or on a network filesystem
shared by several daemons.

## Configuration

Map a cache mount ID to an absolute host path in [buildkitd.toml](buildkitd.toml.md):

```toml
[cacheMounts."gocache"]
  path = "/var/cache/buildkit/gocache"

[cacheMounts."apt"]
  path = "/var/cache/buildkit/apt"
```

Every build on this daemon that mounts one of these IDs uses the configured
directory:

```dockerfile
RUN --mount=type=cache,id=gocache,target=/root/.cache/go-build go build ./...
```

Builds select a directory only by cache mount ID. Host paths are taken from the
daemon configuration and can't be set by a build.

The Dockerfile frontend prefixes cache mount IDs with `BUILDKIT_CACHE_MOUNT_NS`.
When it's unset, the `gocache` key matches `id=gocache`. When it's set to `ns`,
use the key `ns/gocache`. A cache mount without an `id` uses its target path as
the ID, so `target=/root/.cache` matches the key `/root/.cache`, or
`ns/root/.cache` with a namespace. IDs are compared as paths: leading and
repeated slashes are ignored, so `/gocache` and `gocache` are the same key.

`buildkitd` creates each configured directory when it doesn't exist, and
refuses to start if a configured directory is not owned by the user
`buildkitd` runs as, or is accessible by group or others: build output lands
there as-is, including setuid binaries and device nodes that builds create. Two IDs can't use the same or nested directories, also
through symbolic links.

## Directory layout

Each configured directory holds numbered instances, each with a lock file:

```text
/var/cache/buildkit/gocache/
├── 0/              # cache contents mounted into builds
├── 0.lock
├── 0.retired       # only while a reset is pending for an instance in use
├── 0.retired.lock
├── 1/              # only created when instance 0 was busy, see sharing modes
├── 1.lock
├── .create.lock    # serializes creating instances
└── base-1000-1000-0750-<hash>/   # the same layout, for mounts with uid/gid/mode
```

To seed a cache, populate `0/` while no build uses the directory, and make
sure there is no `0.retired` next to it: an instance with that marker is
discarded. Lock files can stay in place; don't remove them while a daemon is
running. An instance must be a real directory: `buildkitd` refuses to use one
that is a symbolic link.

## Sharing modes and multiple daemons

Daemons coordinate through `flock(2)` locks on the `*.lock` files, so the
sharing modes apply across every daemon using the directory:

| Mode               | Behavior                                                                                                  |
|--------------------|-----------------------------------------------------------------------------------------------------------|
| `sharing=shared`   | Any number of builds use instance `0` at the same time. If a `locked` or `private` build holds it, a different instance is used. |
| `sharing=locked`   | One build at a time per instance. Other `locked` builds wait.                                             |
| `sharing=private`  | One build at a time per instance. Other `private` builds use, or create, another instance instead of waiting. |

When the directory is on a network filesystem, the filesystem must support
`flock(2)` across clients, such as NFSv4. Otherwise `locked` and `private`
builds on different daemons can use the same instance at once.

All daemons that share a directory must run as the same user with the same
user namespace mapping. Lock files and instances are created by, and only
accessible to, the daemon's user, and ownership inside instances would not
line up between different mappings.

## Cache mounts with `uid`, `gid` or `mode`

A Dockerfile cache mount with `uid`, `gid` or `mode` starts from a directory
with that ownership and mode. Each combination gets its own set of instances
in a `base-*` subdirectory, so mounts of one ID with different options are
separate caches, as with snapshot cache mounts.

A cache mount that starts from a populated directory, `from=<stage or image>`
with `source=`, is rejected when its ID is host-backed.

## Interaction with cache management

- `--no-cache`, and other ways of ignoring the cache of a step that has the
  cache mount, reset the configured directory for that ID, including its
  `base-*` subdirectories. An instance that another build is using at that
  moment keeps its files until that build finishes, but is retired: no later
  build uses it, so the build that asked for the reset starts empty.
- `buildctl du` doesn't report host cache mounts, and `buildctl prune` and
  garbage collection don't remove them. Size and clean up the directories
  yourself, for example by deleting the instance directories while no build
  is running.
- Rootless `buildkitd` must own the configured directories, like any other
  `buildkitd`. On a network filesystem, ownership must be preserved: a rootful
  `buildkitd` on an NFS export with `root_squash` is refused.

Host cache mounts aren't supported on Windows.
