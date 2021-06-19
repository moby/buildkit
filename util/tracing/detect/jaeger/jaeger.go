package jaeger

import (
	"net"
	"os"
	"strings"

	"github.com/moby/buildkit/util/tracing/detect"
	"go.opentelemetry.io/otel/exporters/jaeger"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func init() {
	detect.Register("jaeger", jaegerExporter)
}

func jaegerExporter() (sdktrace.SpanExporter, error) {
	set := os.Getenv("OTEL_TRACES_EXPORTER") == "jaeger" || os.Getenv("JAEGER_TRACE") != "" || os.Getenv("OTEL_EXPORTER_JAEGER_AGENT_HOST") != "" || os.Getenv("OTEL_EXPORTER_JAEGER_ENDPOINT") != ""
	if !set {
		return nil, nil
	}

	endpoint := envOr("OTEL_EXPORTER_JAEGER_ENDPOINT", "http://localhost:14250")
	host := envOr("OTEL_EXPORTER_JAEGER_HOST", "localhost")
	port := envOr("OTEL_EXPORTER_JAEGER_PORT", "6831")
	var isEndpoint bool

	// JAEGER_TRACE is not env defined by opentelemetry spec but buildkit backward compatibility
	if v := os.Getenv("JAEGER_TRACE"); v != "" {
		if strings.HasPrefix(v, "http://") || strings.HasPrefix(v, "https://") {
			isEndpoint = true
			endpoint = v
		} else {
			h, p, err := net.SplitHostPort(v)
			if err != nil {
				return nil, err
			}
			host = h
			port = p
		}
	} else {
		isEndpoint = os.Getenv("OTEL_EXPORTER_JAEGER_ENDPOINT") != ""
	}

	epo := jaeger.WithCollectorEndpoint(jaeger.WithEndpoint(endpoint))

	if !isEndpoint {
		epo = jaeger.WithAgentEndpoint(jaeger.WithAgentHost(host), jaeger.WithAgentPort(port))
	}

	return jaeger.New(epo)
}

func envOr(key, defaultValue string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return defaultValue
}
