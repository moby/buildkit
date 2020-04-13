package main

import (
	"io"
	"os"

	opentracing "github.com/opentracing/opentracing-go"
	jaeger "github.com/uber/jaeger-client-go"
	"go.undefinedlabs.com/scopeagent/env"
	"go.undefinedlabs.com/scopeagent/instrumentation"
)

var tracer opentracing.Tracer
var closeTracer io.Closer

type nopCloser struct {
}

func (*nopCloser) Close() error {
	return nil
}

func init() {

	tracer = opentracing.NoopTracer{}

	if env.ScopeDsn.Value != "" {
		tracer = instrumentation.Tracer()
		closeTracer = &nopCloser{}

	} else if traceAddr := os.Getenv("JAEGER_TRACE"); traceAddr != "" {
		tr, err := jaeger.NewUDPTransport(traceAddr, 0)
		if err != nil {
			panic(err)
		}

		tracer, closeTracer = jaeger.NewTracer(
			"buildkitd",
			jaeger.NewConstSampler(true),
			jaeger.NewRemoteReporter(tr),
		)
	}

}
