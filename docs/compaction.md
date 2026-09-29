# Metadata database compaction

BuildKit cache GC removes cache records, but bbolt retains freed pages inside its
files for reuse. Compaction copies live data into a smaller file to return unused
space to the filesystem. It runs independently of cache GC and covers `cache.db`,
`history.db`, and the worker databases `metadata_v2.db` and `containerdmeta.db`.
The optional `cache-debug.db` and snapshotter-owned `snapshots/metadata.db` are not
included.

## Configuration and scheduling

Automatic compaction is disabled by default. Enable it in
[buildkitd.toml](buildkitd.toml.md) with `[compaction].enabled = true`.
The remaining defaults are:

| Setting             | Default     | Meaning                                                                  |
|---------------------|-------------|--------------------------------------------------------------------------|
| `writesPerCheck`    | `10000`     | Committed write transactions between automatic eligibility checks.       |
| `sizeWatermark`     | `134217728` | Initial database size watermark (128 MiB).                               |
| `sizeGrowthPercent` | `100`       | Growth over the compacted size before the next size-triggered check.     |
| `minReclaimBytes`   | `268435456` | Minimum estimated reclaimable bytes (256 MiB).                           |
| `minReclaimPercent` | `25`        | Minimum estimated reclaimable percentage of the file.                    |
| `idleTimeout`       | `1m`        | Required interval without database activity.                             |
| `maxRetry`          | `3`         | Attempts that arriving writers may cancel before a copy makes them wait. |

The policy checks database size at most every five minutes, after committed
writes. Reaching the size watermark makes the database eligible for a
reclaimability check. After a successful compaction, the next watermark is the
compacted size plus `sizeGrowthPercent`, with `sizeWatermark` as the minimum.

Every `writesPerCheck` committed writes, reclaimability is checked without waiting
for the adaptive watermark, while the configured initial watermark remains the
minimum file size. This catches space released by deletions when bbolt reuses free
pages without growing the file. Idle databases are not polled.

Either reclaimability threshold must be met before compaction becomes pending.
Deletion writes can therefore make a database eligible without further file growth.
Pending maintenance waits for no active transactions and the configured idle
period; reads and writes both count as activity.

Copies are serialized across databases in the daemon. Before copying, the wrapper
drains transactions and checks reclaimability and filesystem headroom again.
New transactions wait during the copy. Arriving writers cancel automatic attempts
up to `maxRetry`; subsequent attempts let the copy finish while transactions wait.
Setting `maxRetry = 0` makes the first automatic attempt follow that behavior.

When changed, the write counter and adaptive size watermark are checkpointed
beside each database every five minutes and during orderly shutdown.

## Manual compaction

The [debug HTTP listener](dev/debug-endpoints.md#metadata-database-compaction) provides
inspection and streamed manual attempts independently of automatic scheduling.
Enabling the listener is sufficient to make these operations available, even with
`[compaction].enabled = false`. Manual attempts bypass `writesPerCheck` and
`sizeWatermark` but retain the idle period, reclaimability thresholds, and
free-space checks. They remain cancellable by arriving writers and never escalate
to forced copies.

## Operational limits

Automatic copies have no copy deadline after writer-cancellation retries are
exhausted, so transaction pauses can be substantial for large live datasets.
Continuously active databases may never reach the required idle period. Measure
copy duration on representative storage before choosing production settings.

Compaction needs enough free space for a replacement file alongside the original.
It cannot by itself recover an already-full volume, reclaim retained live data,
or make cache GC account for database files.

## Metrics

Compaction uses the daemon's existing OpenTelemetry meter provider. Prometheus
metrics are available at `/metrics` on the debug listener and are also available
through configured OpenTelemetry metric exporters. No additional exporter or
listener is required.

| OpenTelemetry instrument                       | Type      | Unit     |
|------------------------------------------------|-----------|----------|
| `buildkit.compaction.attempts`                 | Counter   | Attempts |
| `buildkit.compaction.copy.duration`            | Histogram | Seconds  |
| `buildkit.compaction.reclaimed`                | Counter   | Bytes    |
| `buildkit.compaction.pending.duration`         | Gauge     | Seconds  |
| `buildkit.compaction.database.size`            | Gauge     | Bytes    |
| `buildkit.compaction.database.reclaimable`     | Gauge     | Bytes    |
| `buildkit.compaction.database.observation.age` | Gauge     | Seconds  |

Attempts and copy duration use labels for database path, trigger
(`automatic` or `manual`), and outcome (`completed`, `skipped`, `canceled`, or
`failed`). Reclaimed bytes use path and trigger. An attempt is counted when the
backend is invoked; eligibility checks and requests canceled while waiting are not
attempts. Copy duration includes replacement, canceled and failed copies, but
excludes waiting and transaction draining. Reclaimed bytes are recorded only when replacement
succeeded and the resulting size is known.

The `database.path` label is relative to the BuildKit state directory, for example
`cache.db` or `runc-overlayfs/metadata_v2.db`, with forward slashes on all platforms.
Absolute paths are not exposed. Databases without a path or outside the state
directory use an empty label. Worker databases have separate series.
Size and reclaimable gauges report cached observations by database path.
Observation age reports the age of those measurements. Unobserved
databases contribute no size sample, and closed databases are removed. Pending
duration reports the longest current wait by path and trigger, resetting when an
attempt starts or its request is canceled.

Collection only reads cached measurements. Policy checks, debug inspection, and
attempt completion refresh them without adding per-transaction instrumentation.
In manual-only mode, sizes can remain unobserved or stale until inspection or an
attempt; collection does not poll the database or filesystem.

The Prometheus exporter translates instrument names and units, for example
`buildkit.compaction.database.size` becomes
`buildkit_compaction_database_size_bytes`, and `database.path` becomes
`database_path`.
