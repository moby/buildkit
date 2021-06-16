# OpenTelemetry-Go Jaeger Exporter

OpenTelemetry Jaeger exporter 

## Installation
```
go get -u go.opentelemetry.io/otel/exporters/trace/jaeger
```

## Maintenance

This exporter uses a vendored copy of the Apache Thrift library (v0.14.1) at a custom import path. When re-generating Thrift code in future, please adapt import paths as necessary.
