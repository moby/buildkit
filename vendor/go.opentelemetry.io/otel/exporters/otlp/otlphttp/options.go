// Copyright The OpenTelemetry Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package otlphttp

import (
	"crypto/tls"
	"time"

	"go.opentelemetry.io/otel/exporters/otlp"
	"go.opentelemetry.io/otel/exporters/otlp/internal/otlpconfig"
)

const (
	// DefaultMaxAttempts describes how many times the driver
	// should retry the sending of the payload in case of a
	// retryable error.
	DefaultMaxAttempts int = 5
	// DefaultTracesPath is a default URL path for endpoint that
	// receives spans.
	DefaultTracesPath string = "/v1/traces"
	// DefaultMetricsPath is a default URL path for endpoint that
	// receives metrics.
	DefaultMetricsPath string = "/v1/metrics"
	// DefaultBackoff is a default base backoff time used in the
	// exponential backoff strategy.
	DefaultBackoff time.Duration = 300 * time.Millisecond
	// DefaultTimeout is a default max waiting time for the backend to process
	// each span or metrics batch.
	DefaultTimeout time.Duration = 10 * time.Second
)

// Option applies an option to the HTTP driver.
type Option interface {
	otlpconfig.HTTPOption
}

// WithEndpoint allows one to set the address of the collector
// endpoint that the driver will use to send metrics and spans. If
// unset, it will instead try to use
// DefaultCollectorHost:DefaultCollectorPort. Note that the endpoint
// must not contain any URL path.
func WithEndpoint(endpoint string) Option {
	return otlpconfig.WithEndpoint(endpoint)
}

// WithTracesEndpoint allows one to set the address of the collector
// endpoint that the driver will use to send spans. If
// unset, it will instead try to use the Endpoint configuration.
// Note that the endpoint must not contain any URL path.
func WithTracesEndpoint(endpoint string) Option {
	return otlpconfig.WithTracesEndpoint(endpoint)
}

// WithMetricsEndpoint allows one to set the address of the collector
// endpoint that the driver will use to send metrics. If
// unset, it will instead try to use the Endpoint configuration.
// Note that the endpoint must not contain any URL path.
func WithMetricsEndpoint(endpoint string) Option {
	return otlpconfig.WithMetricsEndpoint(endpoint)
}

// WithCompression tells the driver to compress the sent data.
func WithCompression(compression otlp.Compression) Option {
	return otlpconfig.WithCompression(compression)
}

// WithTracesCompression tells the driver to compress the sent traces data.
func WithTracesCompression(compression otlp.Compression) Option {
	return otlpconfig.WithTracesCompression(compression)
}

// WithMetricsCompression tells the driver to compress the sent metrics data.
func WithMetricsCompression(compression otlp.Compression) Option {
	return otlpconfig.WithMetricsCompression(compression)
}

// WithTracesURLPath allows one to override the default URL path used
// for sending traces. If unset, DefaultTracesPath will be used.
func WithTracesURLPath(urlPath string) Option {
	return otlpconfig.WithTracesURLPath(urlPath)
}

// WithMetricsURLPath allows one to override the default URL path used
// for sending metrics. If unset, DefaultMetricsPath will be used.
func WithMetricsURLPath(urlPath string) Option {
	return otlpconfig.WithMetricsURLPath(urlPath)
}

// WithMaxAttempts allows one to override how many times the driver
// will try to send the payload in case of retryable errors. If unset,
// DefaultMaxAttempts will be used.
func WithMaxAttempts(maxAttempts int) Option {
	return otlpconfig.WithMaxAttempts(maxAttempts)
}

// WithBackoff tells the driver to use the duration as a base of the
// exponential backoff strategy. If unset, DefaultBackoff will be
// used.
func WithBackoff(duration time.Duration) Option {
	return otlpconfig.WithBackoff(duration)
}

// WithTLSClientConfig can be used to set up a custom TLS
// configuration for the client used to send payloads to the
// collector. Use it if you want to use a custom certificate.
func WithTLSClientConfig(tlsCfg *tls.Config) Option {
	return otlpconfig.WithTLSClientConfig(tlsCfg)
}

// WithTracesTLSClientConfig can be used to set up a custom TLS
// configuration for the client used to send traces.
// Use it if you want to use a custom certificate.
func WithTracesTLSClientConfig(tlsCfg *tls.Config) Option {
	return otlpconfig.WithTracesTLSClientConfig(tlsCfg)
}

// WithMetricsTLSClientConfig can be used to set up a custom TLS
// configuration for the client used to send metrics.
// Use it if you want to use a custom certificate.
func WithMetricsTLSClientConfig(tlsCfg *tls.Config) Option {
	return otlpconfig.WithMetricsTLSClientConfig(tlsCfg)
}

// WithInsecure tells the driver to connect to the collector using the
// HTTP scheme, instead of HTTPS.
func WithInsecure() Option {
	return otlpconfig.WithInsecure()
}

// WithInsecureTraces tells the driver to connect to the traces collector using the
// HTTP scheme, instead of HTTPS.
func WithInsecureTraces() Option {
	return otlpconfig.WithInsecureTraces()
}

// WithInsecure tells the driver to connect to the metrics collector using the
// HTTP scheme, instead of HTTPS.
func WithInsecureMetrics() Option {
	return otlpconfig.WithInsecureMetrics()
}

// WithHeaders allows one to tell the driver to send additional HTTP
// headers with the payloads. Specifying headers like Content-Length,
// Content-Encoding and Content-Type may result in a broken driver.
func WithHeaders(headers map[string]string) Option {
	return otlpconfig.WithHeaders(headers)
}

// WithTracesHeaders allows one to tell the driver to send additional HTTP
// headers with the trace payloads. Specifying headers like Content-Length,
// Content-Encoding and Content-Type may result in a broken driver.
func WithTracesHeaders(headers map[string]string) Option {
	return otlpconfig.WithTracesHeaders(headers)
}

// WithMetricsHeaders allows one to tell the driver to send additional HTTP
// headers with the metrics payloads. Specifying headers like Content-Length,
// Content-Encoding and Content-Type may result in a broken driver.
func WithMetricsHeaders(headers map[string]string) Option {
	return otlpconfig.WithMetricsHeaders(headers)
}

// WithMarshal tells the driver which wire format to use when sending to the
// collector.  If unset, MarshalProto will be used
func WithMarshal(m otlp.Marshaler) Option {
	return otlpconfig.NewHTTPOption(func(cfg *otlpconfig.Config) {
		cfg.Marshaler = m
	})
}

// WithTimeout tells the driver the max waiting time for the backend to process
// each spans or metrics batch.  If unset, the default will be 10 seconds.
func WithTimeout(duration time.Duration) Option {
	return otlpconfig.WithTimeout(duration)
}

// WithTracesTimeout tells the driver the max waiting time for the backend to process
// each spans batch.  If unset, the default will be 10 seconds.
func WithTracesTimeout(duration time.Duration) Option {
	return otlpconfig.WithTracesTimeout(duration)
}

// WithMetricsTimeout tells the driver the max waiting time for the backend to process
// each metrics batch.  If unset, the default will be 10 seconds.
func WithMetricsTimeout(duration time.Duration) Option {
	return otlpconfig.WithMetricsTimeout(duration)
}
