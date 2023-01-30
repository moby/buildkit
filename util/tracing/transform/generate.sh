#!/bin/sh
# Sync the files from ../../../vendor/go.opentelemetry.io/otel/exporters/otlp/otlptrace/internal/tracetransform/*.go
set -eu
for f in ../../../vendor/go.opentelemetry.io/otel/exporters/otlp/otlptrace/internal/tracetransform/*.go; do
	ff="$(basename "${f}")"
	cp "${f}" "${ff}"
	sed -i -e 's@package tracetransform // import "go.opentelemetry.io/otel/exporters/otlp/otlptrace/internal/tracetransform"@package transform@g' "${ff}"
done
