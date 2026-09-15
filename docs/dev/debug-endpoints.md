# Debug HTTP endpoints

Enable the debug listener with `buildkitd --debugaddr 127.0.0.1:6060` or the
equivalent [buildkitd configuration](../buildkitd.toml.md):

```toml
[grpc]
  debugAddress = "127.0.0.1:6060"
```

The listener serves HTTP diagnostics such as Go profiles under `/debug/pprof/`
and the maintenance endpoints below. Access is controlled by the listener address;
these endpoints do not add authentication. Keep the listener restricted to trusted
clients. Compaction POST requests reject cross-origin browser requests; this does
not authenticate non-browser clients. The `--debug` flag controls logging and does
not enable this listener.

## Metadata database compaction

Enabling the debug listener makes manual compaction available even when
`[compaction].enabled` is false. That setting controls automatic scheduling only.
The configured idle timeout and reclaimability thresholds apply to manual attempts
as well. With neither automatic compaction nor the debug listener enabled, no
compaction scheduler is attached.

`GET /debug/compaction` reports registered databases, policy state, reclaimable
space, and estimated copy headroom without scheduling maintenance. The measurements
are advisory and are checked again when a copy starts.

```sh
curl http://127.0.0.1:6060/debug/compaction
```

`POST /debug/compaction?database=<path>` requests one manual attempt for a database
listed by GET. It bypasses the write watermark but retains the idle period,
reclaimability thresholds, free-space checks, and serialization with other copies.
This is not a dry run. An arriving writer or a disconnected client cancels the
attempt cooperatively; manual attempts never escalate to forced copies. A second
request for the same database returns HTTP 409 while maintenance is in progress.

The response streams phase changes and a final result with file sizes and copy
duration. Use the database path reported by GET:

```sh
curl -N -X POST 'http://127.0.0.1:6060/debug/compaction?database=/var/lib/buildkit/cache.db'
```
