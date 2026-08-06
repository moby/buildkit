# Metrics

buildkitd exports metrics through its OpenTelemetry `MeterProvider`. The same
provider drives both a Prometheus pull endpoint and, when configured, an OTLP
push exporter, so the instruments below are available through either transport.

## Enabling metrics

### Prometheus (pull)

buildkitd registers a Prometheus exporter on its debug HTTP server. Start the
daemon with a debug address and scrape `/metrics`:

```bash
buildkitd --debugaddr 127.0.0.1:6060
curl http://127.0.0.1:6060/metrics
```

The debug endpoint also exposes Go runtime and profiling handlers, so bind it
to a trusted interface and put it behind your own access controls.

OpenTelemetry instrument names are mangled to Prometheus conventions on this
endpoint: dots become underscores, monotonic counters gain a `_total` suffix,
and the unit is appended (for example `buildkit.build.duration` with unit `s`
is exported as `buildkit_build_duration_seconds`). The build duration is an
exponential histogram and is exported as a Prometheus native histogram, which
requires a scraper that negotiates the Prometheus protobuf format.

### OTLP (push)

Set the standard OpenTelemetry environment variables to push metrics to a
collector, for example:

```bash
export OTEL_EXPORTER_OTLP_METRICS_ENDPOINT=http://collector:4317
```

Set `OTEL_METRICS_EXPORTER=none` to disable metrics export entirely.

## Instruments

| Name | Type | Unit | Attributes | Description |
| --- | --- | --- | --- | --- |
| `buildkit.builds` | counter | | `status`, `error_code` | Builds completed. `status` is `success` or `failure`; `error_code` carries the gRPC status code string and is present only on failure. |
| `buildkit.builds.steps` | counter | | `kind` | Build steps observed, partitioned by `kind`: `completed`, `cached`, `total`, `warnings`. |
| `buildkit.build.duration` | histogram | `s` | `status` | Wall-clock duration of build solves, from creation to completion. Exported as an exponential (native) histogram. |
| `buildkit.workers` | gauge | | | Number of workers currently registered with the daemon. |

## Attributes and cardinality

Attributes are restricted to bounded, enumerated values so that the number of
exported time series stays finite. Free-form values such as error messages,
frontend identifiers, or per-build identifiers are intentionally not used as
attributes.
