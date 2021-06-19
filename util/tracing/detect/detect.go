package detect

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/pkg/errors"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.4.0"
	"go.opentelemetry.io/otel/trace"
)

type ExporterDetector func() (sdktrace.SpanExporter, error)

var ServiceName string

var detectors map[string]ExporterDetector
var once sync.Once
var tp trace.TracerProvider
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
	tp = trace.NewNoopTracerProvider()

	exp, err := detectExporter()
	if err != nil {
		return err
	}

	if exp == nil {
		return nil
	}
	res, err := resource.Detect(context.Background(), serviceNameDetector{})
	if err != nil {
		return err
	}
	res, err = resource.Merge(resource.Default(), res)
	if err != nil {
		return err
	}

	sp := sdktrace.NewBatchSpanProcessor(exp)

	sdktp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sp), sdktrace.WithResource(res))
	closers = append(closers, sdktp.Shutdown)
	tp = sdktp

	return nil
}

func TracerProvider() (trace.TracerProvider, error) {
	once.Do(func() {
		if err1 := detect(); err1 != nil {
			err = err1
		}
	})
	b, _ := strconv.ParseBool(os.Getenv("OTEL_INGORE_ERROR"))
	if err != nil && !b {
		return nil, err
	}
	return tp, nil
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
		semconv.SchemaURL,
		semconv.ServiceNameKey,
		func() (string, error) {
			if n := os.Getenv("OTEL_SERVICE_NAME"); n != "" {
				return n, nil
			}
			if ServiceName != "" {
				return ServiceName, nil
			}
			return filepath.Base(os.Args[0]), nil
		},
	).Detect(ctx)
}
