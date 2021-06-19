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

package otlptracehttp

import (
	"crypto/tls"
	"time"

	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/internal/otlpconfig"
)

const (
	// defaultMaxAttempts describes how many times the driver
	// should retry the sending of the payload in case of a
	// retryable error.
	defaultMaxAttempts int = 5
	// defaultTracesPath is a default URL path for endpoint that
	// receives spans.
	defaultTracesPath string = "/v1/traces"
	// defaultBackoff is a default base backoff time used in the
	// exponential backoff strategy.
	defaultBackoff time.Duration = 300 * time.Millisecond
)

// Compression describes the compression used for payloads sent to the
// collector.
type Compression otlpconfig.Compression

const (
	// NoCompression tells the driver to send payloads without
	// compression.
	NoCompression = Compression(otlpconfig.NoCompression)
	// GzipCompression tells the driver to send payloads after
	// compressing them with gzip.
	GzipCompression = Compression(otlpconfig.GzipCompression)
)

// Option applies an option to the HTTP client.
type Option interface {
	applyHTTPOption(*otlpconfig.Config)
}

// RetrySettings defines configuration for retrying batches in case of export failure
// using an exponential backoff.
type RetrySettings otlpconfig.RetrySettings

type wrappedOption struct {
	otlpconfig.HTTPOption
}

func (w wrappedOption) applyHTTPOption(cfg *otlpconfig.Config) {
	w.ApplyHTTPOption(cfg)
}

// WithEndpoint allows one to set the address of the collector
// endpoint that the driver will use to send spans. If
// unset, it will instead try to use
// the default endpoint (localhost:4317). Note that the endpoint
// must not contain any URL path.
func WithEndpoint(endpoint string) Option {
	return wrappedOption{otlpconfig.WithEndpoint(endpoint)}
}

// WithCompression tells the driver to compress the sent data.
func WithCompression(compression Compression) Option {
	return wrappedOption{otlpconfig.WithCompression(otlpconfig.Compression(compression))}
}

// WithURLPath allows one to override the default URL path used
// for sending traces. If unset, default ("/v1/traces") will be used.
func WithURLPath(urlPath string) Option {
	return wrappedOption{otlpconfig.WithURLPath(urlPath)}
}

// WithMaxAttempts allows one to override how many times the driver
// will try to send the payload in case of retryable errors.
// The max attempts is limited to at most 5 retries. If unset,
// default (5) will be used.
func WithMaxAttempts(maxAttempts int) Option {
	return wrappedOption{otlpconfig.WithMaxAttempts(maxAttempts)}
}

// WithBackoff tells the driver to use the duration as a base of the
// exponential backoff strategy. If unset, default (300ms) will be
// used.
func WithBackoff(duration time.Duration) Option {
	return wrappedOption{otlpconfig.WithBackoff(duration)}
}

// WithTLSClientConfig can be used to set up a custom TLS
// configuration for the client used to send payloads to the
// collector. Use it if you want to use a custom certificate.
func WithTLSClientConfig(tlsCfg *tls.Config) Option {
	return wrappedOption{otlpconfig.WithTLSClientConfig(tlsCfg)}
}

// WithInsecure tells the driver to connect to the collector using the
// HTTP scheme, instead of HTTPS.
func WithInsecure() Option {
	return wrappedOption{otlpconfig.WithInsecure()}
}

// WithHeaders allows one to tell the driver to send additional HTTP
// headers with the payloads. Specifying headers like Content-Length,
// Content-Encoding and Content-Type may result in a broken driver.
func WithHeaders(headers map[string]string) Option {
	return wrappedOption{otlpconfig.WithHeaders(headers)}
}

// WithTimeout tells the driver the max waiting time for the backend to process
// each spans batch.  If unset, the default will be 10 seconds.
func WithTimeout(duration time.Duration) Option {
	return wrappedOption{otlpconfig.WithTimeout(duration)}
}
