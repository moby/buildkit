package detect

import (
	"context"
	"os"
	"strconv"
	"sync"

	"github.com/opentracing/opentracing-go"
	"github.com/pkg/errors"
	otbridge "go.opentelemetry.io/otel/bridge/opentracing"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/semconv"
	"go.opentelemetry.io/otel/trace"
)

type ExporterDetector func() (sdktrace.SpanExporter, error)

var detectors map[string]ExporterDetector
var once sync.Once
var tracer trace.Tracer
var otTracer opentracing.Tracer
var exporter sdktrace.SpanExporter
var closers []func(context.Context) error
var err error

func Register(name string, exp ExporterDetector) {
	if detectors == nil {
		detectors = map[string]ExporterDetector{}
	}
	detectors[name] = exp
}

func detectExporter() (sdktrace.SpanExporter, error) {
	if n := os.Getenv("OTEL_TRACES_EXPORTER"); n != "" {
		d, ok := detectors[n]
		if !ok {
			if n == "none" {
				return nil, nil
			}
			return nil, errors.Errorf("unsupported opentelemetry tracer %v", n)
		}
		return d()
	}
	for _, d := range detectors {
		exp, err := d()
		if err != nil {
			return nil, err
		}
		if exp != nil {
			return exp, nil
		}
	}
	return nil, nil
}

func detect() error {
	tp := trace.NewNoopTracerProvider()
	tracer = tp.Tracer("")
	otTracer = opentracing.NoopTracer{}

	exp, err := detectExporter()
	if err != nil {
		return err
	}

	if exp == nil {
		return nil
	}

	res, err := resource.Detect(context.Background(), serviceNameDetector{}, resource.FromEnv{}, resource.Host{}, resource.TelemetrySDK{})
	if err != nil {
		return err
	}
	exporter = exp

	sp := sdktrace.NewBatchSpanProcessor(exp)

	closers = append(closers, sp.Shutdown)

	tp = sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sp), sdktrace.WithResource(res))
	tracer = tp.Tracer("")

	bt, wt := otbridge.NewTracerPair(tracer)
	otTracer = bt
	tracer = wt.Tracer("")

	return nil
}

func tracers() (trace.Tracer, opentracing.Tracer, error) {
	once.Do(func() {
		if err1 := detect(); err1 != nil {
			err = err1
		}
	})
	b, _ := strconv.ParseBool(os.Getenv("OTEL_INGORE_ERROR"))
	if err != nil && !b {
		return nil, nil, err
	}
	return tracer, otTracer, nil
}

func Tracer() (trace.Tracer, error) {
	t, _, err := tracers()
	return t, err
}

func OTTracer() (opentracing.Tracer, error) {
	_, t, err := tracers()
	return t, err
}

func Shutdown(ctx context.Context) error {
	for _, c := range closers {
		if err := c(ctx); err != nil {
			return err
		}
	}
	return nil
}

type serviceNameDetector struct{}

func (serviceNameDetector) Detect(ctx context.Context) (*resource.Resource, error) {
	return resource.StringDetector(
		semconv.ServiceNameKey,
		func() (string, error) {
			if n := os.Getenv("OTEL_SERVICE_NAME"); n != "" {
				return n, nil
			}
			return os.Args[0], nil
		},
	).Detect(ctx)
}
