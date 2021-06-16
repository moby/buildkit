package detect

import (
	"context"
	"os"

	"github.com/pkg/errors"
	"go.opentelemetry.io/otel/exporters/otlp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlphttp"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func init() {
	Register("otlp", otlpExporter)
}

func otlpExporter() (sdktrace.SpanExporter, error) {
	set := os.Getenv("OTEL_TRACES_EXPORTER") == "otpl" || os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" || os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") != ""
	if !set {
		return nil, nil
	}

	proto := os.Getenv("OTEL_EXPORTER_OTLP_TRACES_PROTOCOL")
	if proto == "" {
		proto = os.Getenv("OTEL_EXPORTER_OTLP_PROTOCOL")
	}
	if proto == "" {
		proto = "grpc"
	}

	var pd otlp.ProtocolDriver

	switch proto {
	case "grpc":
		pd = otlpgrpc.NewDriver()
	case "http/protobuf":
		pd = otlphttp.NewDriver(otlphttp.WithMarshal(otlp.MarshalProto))
	case "http/json":
		pd = otlphttp.NewDriver(otlphttp.WithMarshal(otlp.MarshalJSON))
	default:
		return nil, errors.Errorf("unsupported otlp protocol %v", proto)
	}

	return otlp.NewExporter(context.Background(), pd)
}
