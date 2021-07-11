[![Go Reference](https://pkg.go.dev/badge/github.com/coryb/otelbundle/instrumentation/httptrace.svg)](https://pkg.go.dev/github.com/coryb/otelbundle/instrumentation/httptrace)

This is a Go [http-tracing](https://blog.golang.org/http-tracing) library that will annotate OpenTelemetry spans.

You may want to use the official http-tracing support instead:
go.opentelemetry.io/contrib/instrumentation/net/http/httptrace

This package differs from the official tracing solution in that it just adds 
"event" annotations to the span rather than creating many sub-spans as the official one does.

I found dealing with the sub-spans harder to follow when debugging processes with many
http calls.  This approach is similar to how the Zipkin tracing collects http trace data
when tracing is enabled [via the http.Transport](https://pkg.go.dev/github.com/openzipkin/zipkin-go@v0.2.5/middleware/http#TransportTraces).
