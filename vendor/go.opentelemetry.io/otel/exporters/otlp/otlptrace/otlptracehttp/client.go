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
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"math/rand"
	"net"
	"net/http"
	"path"
	"strings"
	"time"

	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/internal/otlpconfig"

	"google.golang.org/protobuf/proto"

	"go.opentelemetry.io/otel"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
)

const contentTypeProto = "application/x-protobuf"

// Keep it in sync with golang's DefaultTransport from net/http! We
// have our own copy to avoid handling a situation where the
// DefaultTransport is overwritten with some different implementation
// of http.RoundTripper or it's modified by other package.
var ourTransport = &http.Transport{
	Proxy: http.ProxyFromEnvironment,
	DialContext: (&net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}).DialContext,
	ForceAttemptHTTP2:     true,
	MaxIdleConns:          100,
	IdleConnTimeout:       90 * time.Second,
	TLSHandshakeTimeout:   10 * time.Second,
	ExpectContinueTimeout: 1 * time.Second,
}

type client struct {
	name       string
	cfg        otlpconfig.SignalConfig
	generalCfg otlpconfig.Config
	client     *http.Client
	stopCh     chan struct{}
}

var _ otlptrace.Client = (*client)(nil)

// NewClient creates a new HTTP trace client.
func NewClient(opts ...Option) otlptrace.Client {
	cfg := otlpconfig.NewDefaultConfig()
	otlpconfig.ApplyHTTPEnvConfigs(&cfg)
	for _, opt := range opts {
		opt.applyHTTPOption(&cfg)
	}

	for pathPtr, defaultPath := range map[*string]string{
		&cfg.Traces.URLPath: defaultTracesPath,
	} {
		tmp := strings.TrimSpace(*pathPtr)
		if tmp == "" {
			tmp = defaultPath
		} else {
			tmp = path.Clean(tmp)
			if !path.IsAbs(tmp) {
				tmp = fmt.Sprintf("/%s", tmp)
			}
		}
		*pathPtr = tmp
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = defaultMaxAttempts
	}
	if cfg.MaxAttempts > defaultMaxAttempts {
		cfg.MaxAttempts = defaultMaxAttempts
	}
	if cfg.Backoff <= 0 {
		cfg.Backoff = defaultBackoff
	}

	httpClient := &http.Client{
		Transport: ourTransport,
		Timeout:   cfg.Traces.Timeout,
	}
	if cfg.Traces.TLSCfg != nil {
		transport := ourTransport.Clone()
		transport.TLSClientConfig = cfg.Traces.TLSCfg
		httpClient.Transport = transport
	}

	stopCh := make(chan struct{})
	return &client{
		name:       "traces",
		cfg:        cfg.Traces,
		generalCfg: cfg,
		stopCh:     stopCh,
		client:     httpClient,
	}
}

// Start does nothing in a HTTP client
func (d *client) Start(ctx context.Context) error {
	// nothing to do
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	return nil
}

// Stop shuts down the client and interrupt any in-flight request.
func (d *client) Stop(ctx context.Context) error {
	close(d.stopCh)
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	return nil
}

// UploadTraces sends a batch of spans to the collector.
func (d *client) UploadTraces(ctx context.Context, protoSpans []*tracepb.ResourceSpans) error {
	pbRequest := &coltracepb.ExportTraceServiceRequest{
		ResourceSpans: protoSpans,
	}
	rawRequest, err := proto.Marshal(pbRequest)
	if err != nil {
		return err
	}
	return d.send(ctx, rawRequest)
}

func (d *client) send(ctx context.Context, rawRequest []byte) error {
	address := fmt.Sprintf("%s://%s%s", d.getScheme(), d.cfg.Endpoint, d.cfg.URLPath)
	var cancel context.CancelFunc
	ctx, cancel = d.contextWithStop(ctx)
	defer cancel()
	for i := 0; i < d.generalCfg.MaxAttempts; i++ {
		response, err := d.singleSend(ctx, rawRequest, address)
		if err != nil {
			return err
		}
		// We don't care about the body, so try to read it
		// into /dev/null and close it immediately. The
		// reading part is to facilitate connection reuse.
		_, _ = io.Copy(ioutil.Discard, response.Body)
		_ = response.Body.Close()
		switch response.StatusCode {
		case http.StatusOK:
			return nil
		case http.StatusTooManyRequests:
			fallthrough
		case http.StatusServiceUnavailable:
			select {
			case <-time.After(getWaitDuration(d.generalCfg.Backoff, i)):
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		default:
			return fmt.Errorf("failed to send %s to %s with HTTP status %s", d.name, address, response.Status)
		}
	}
	return fmt.Errorf("failed to send data to %s after %d tries", address, d.generalCfg.MaxAttempts)
}

func (d *client) getScheme() string {
	if d.cfg.Insecure {
		return "http"
	}
	return "https"
}

func getWaitDuration(backoff time.Duration, i int) time.Duration {
	// Strategy: after nth failed attempt, attempt resending after
	// k * initialBackoff + jitter, where k is a random number in
	// range [0, 2^n-1), and jitter is a random percentage of
	// initialBackoff from [-5%, 5%).
	//
	// Based on
	// https://en.wikipedia.org/wiki/Exponential_backoff#Example_exponential_backoff_algorithm
	//
	// Jitter is our addition.

	// There won't be an overflow, since i is capped to
	// defaultMaxAttempts (5).
	upperK := (int64)(1) << (i + 1)
	jitterPercent := (rand.Float64() - 0.5) / 10.
	jitter := jitterPercent * (float64)(backoff)
	k := rand.Int63n(upperK)
	return (time.Duration)(k)*backoff + (time.Duration)(jitter)
}

func (d *client) contextWithStop(ctx context.Context) (context.Context, context.CancelFunc) {
	// Unify the parent context Done signal with the client's stop
	// channel.
	ctx, cancel := context.WithCancel(ctx)
	go func(ctx context.Context, cancel context.CancelFunc) {
		select {
		case <-ctx.Done():
			// Nothing to do, either cancelled or deadline
			// happened.
		case <-d.stopCh:
			cancel()
		}
	}(ctx, cancel)
	return ctx, cancel
}

func (d *client) singleSend(ctx context.Context, rawRequest []byte, address string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, address, nil)
	if err != nil {
		return nil, err
	}
	bodyReader, contentLength, headers := d.prepareBody(rawRequest)
	// Not closing bodyReader through defer, the HTTP Client's
	// Transport will do it for us
	request.Body = bodyReader
	request.ContentLength = contentLength
	for key, values := range headers {
		for _, value := range values {
			request.Header.Add(key, value)
		}
	}
	return d.client.Do(request)
}

func (d *client) prepareBody(rawRequest []byte) (io.ReadCloser, int64, http.Header) {
	var bodyReader io.ReadCloser
	headers := http.Header{}
	for k, v := range d.cfg.Headers {
		headers.Set(k, v)
	}
	contentLength := (int64)(len(rawRequest))
	headers.Set("Content-Type", contentTypeProto)
	requestReader := bytes.NewBuffer(rawRequest)
	switch Compression(d.cfg.Compression) {
	case NoCompression:
		bodyReader = ioutil.NopCloser(requestReader)
	case GzipCompression:
		preader, pwriter := io.Pipe()
		go func() {
			defer pwriter.Close()
			gzipper := gzip.NewWriter(pwriter)
			defer gzipper.Close()
			_, err := io.Copy(gzipper, requestReader)
			if err != nil {
				otel.Handle(fmt.Errorf("otlphttp: failed to gzip request: %v", err))
			}
		}()
		headers.Set("Content-Encoding", "gzip")
		bodyReader = preader
		contentLength = -1
	}
	return bodyReader, contentLength, headers
}
