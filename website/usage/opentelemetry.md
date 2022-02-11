## OpenTelemetry support

BuildKit supports [OpenTelemetry](https://opentelemetry.io/) for `buildkitd`
gRPC API and `buildctl` commands. To capture the trace to [Jaeger](https://github.com/jaegertracing/jaeger),
set `JAEGER_TRACE` environment variable to the collection address.

```shell
docker run -d -p6831:6831/udp -p16686:16686 jaegertracing/all-in-one:latest
export JAEGER_TRACE=0.0.0.0:6831
# restart buildkitd and buildctl so they know JAEGER_TRACE.
# any buildctl command should be traced to http://127.0.0.1:16686/
```
