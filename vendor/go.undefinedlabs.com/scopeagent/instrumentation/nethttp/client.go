package nethttp

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"strconv"
	"strings"

	"github.com/opentracing/opentracing-go"
	"github.com/opentracing/opentracing-go/ext"
	"github.com/opentracing/opentracing-go/log"

	scopeerrors "go.undefinedlabs.com/scopeagent/errors"
	"go.undefinedlabs.com/scopeagent/instrumentation"
)

type contextKey int

const (
	keyTracer contextKey = iota
)

const defaultComponentName = "net/http"

// Transport wraps a RoundTripper. If a request is being traced with
// Tracer, Transport will inject the current span into the headers,
// and set HTTP related tags on the span.
type Transport struct {
	// The actual RoundTripper to use for the request. A nil
	// RoundTripper defaults to http.DefaultTransport.
	http.RoundTripper

	// Enable payload instrumentation
	PayloadInstrumentation bool

	// Enable stacktrace
	Stacktrace bool
}

type clientOptions struct {
	operationName            string
	componentName            string
	disableClientTrace       bool
	disableInjectSpanContext bool
	spanObserver             func(span opentracing.Span, r *http.Request)
}

// ClientOption controls the behavior of TraceRequest.
type ClientOption func(*clientOptions)

// OperationName returns a ClientOption that sets the operation
// name for the client-side span.
func OperationName(operationName string) ClientOption {
	return func(options *clientOptions) {
		options.operationName = operationName
	}
}

// ComponentName returns a ClientOption that sets the component
// name for the client-side span.
func ComponentName(componentName string) ClientOption {
	return func(options *clientOptions) {
		options.componentName = componentName
	}
}

// ClientTrace returns a ClientOption that turns on or off
// extra instrumentation via httptrace.WithClientTrace.
func ClientTrace(enabled bool) ClientOption {
	return func(options *clientOptions) {
		options.disableClientTrace = !enabled
	}
}

// InjectSpanContext returns a ClientOption that turns on or off
// injection of the Span context in the request HTTP headers.
// If this option is not used, the default behaviour is to
// inject the span context.
func InjectSpanContext(enabled bool) ClientOption {
	return func(options *clientOptions) {
		options.disableInjectSpanContext = !enabled
	}
}

// ClientSpanObserver returns a ClientOption that observes the span
// for the client-side span.
func ClientSpanObserver(f func(span opentracing.Span, r *http.Request)) ClientOption {
	return func(options *clientOptions) {
		options.spanObserver = f
	}
}

// TraceRequest adds a ClientTracer to req, tracing the request and
// all requests caused due to redirects. When tracing requests this
// way you must also use Transport.
//
// Example:
//
// 	func AskGoogle(ctx context.Context) error {
// 		client := &http.Client{Transport: &nethttp.Transport{}}
// 		req, err := http.NewRequest("GET", "http://google.com", nil)
// 		if err != nil {
// 			return err
// 		}
// 		req = req.WithContext(ctx) // extend existing trace, if any
//
// 		req, ht := nethttp.TraceRequest(tracer, req)
// 		defer ht.Finish()
//
// 		res, err := client.Do(req)
// 		if err != nil {
// 			return err
// 		}
// 		res.Body.Close()
// 		return nil
// 	}
func TraceRequest(tr opentracing.Tracer, req *http.Request, options ...ClientOption) (*http.Request, *Tracer) {
	opts := &clientOptions{
		spanObserver: func(_ opentracing.Span, _ *http.Request) {},
	}
	for _, opt := range options {
		opt(opts)
	}
	ht := &Tracer{tr: tr, opts: opts}
	ctx := req.Context()
	if !opts.disableClientTrace {
		ctx = httptrace.WithClientTrace(ctx, ht.clientTrace())
	}
	req = req.WithContext(context.WithValue(ctx, keyTracer, ht))
	return req, ht
}

type closeTracker struct {
	io.ReadCloser
	sp opentracing.Span
}

func (c closeTracker) Close() error {
	err := c.ReadCloser.Close()
	c.sp.Finish()
	return err
}

// TracerFromRequest retrieves the Tracer from the request. If the request does
// not have a Tracer it will return nil.
func TracerFromRequest(req *http.Request) *Tracer {
	tr, ok := req.Context().Value(keyTracer).(*Tracer)
	if !ok {
		return nil
	}
	return tr
}

func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Only trace outgoing requests that are inside an active trace
	parent := opentracing.SpanFromContext(req.Context())
	if parent == nil {
		rt := t.RoundTripper
		if rt == nil {
			rt = http.DefaultTransport
		}
		return rt.RoundTrip(req)
	}
	req, _ = TraceRequest(instrumentation.Tracer(), req)
	return t.doRoundTrip(req)
}

// RoundTrip implements the RoundTripper interface.
func (t *Transport) doRoundTrip(req *http.Request) (*http.Response, error) {
	rt := t.RoundTripper
	if rt == nil {
		rt = http.DefaultTransport
	}
	tracer := TracerFromRequest(req)
	if tracer == nil {
		return rt.RoundTrip(req)
	}

	tracer.start(req)

	if t.Stacktrace {
		tracer.sp.SetTag("stacktrace", scopeerrors.GetCurrentStackTrace(2))
	}

	ext.HTTPMethod.Set(tracer.sp, req.Method)
	ext.HTTPUrl.Set(tracer.sp, req.URL.String())
	tracer.opts.spanObserver(tracer.sp, req)

	if !tracer.opts.disableInjectSpanContext {
		carrier := opentracing.HTTPHeadersCarrier(req.Header)
		tracer.sp.Tracer().Inject(tracer.sp.Context(), opentracing.HTTPHeaders, carrier)
	}

	if t.PayloadInstrumentation {
		rqPayload := getRequestPayload(req, payloadBufferSize)
		tracer.sp.SetTag("http.request_payload", rqPayload)
	} else {
		tracer.sp.SetTag("http.request_payload.unavailable", "disabled")
	}

	resp, err := rt.RoundTrip(req)

	if t.PayloadInstrumentation {
		rsPayLoad := getResponsePayload(resp, payloadBufferSize)
		tracer.sp.SetTag("http.response_payload", rsPayLoad)
	} else {
		tracer.sp.SetTag("http.response_payload.unavailable", "disabled")
	}

	if err != nil {
		tracer.sp.Finish()
		return resp, err
	}
	ext.HTTPStatusCode.Set(tracer.sp, uint16(resp.StatusCode))
	if resp.StatusCode >= http.StatusBadRequest {
		ext.Error.Set(tracer.sp, true)
	}
	if req.Method == "HEAD" {
		tracer.sp.Finish()
	} else {
		resp.Body = closeTracker{resp.Body, tracer.sp}
	}
	return resp, nil
}

// Gets the request payload
func getRequestPayload(req *http.Request, bufferSize int) string {
	if req == nil || req.Body == nil || req.Body == http.NoBody {
		return ""
	}
	if req.GetBody == nil {
		// GetBody is nil in server requests
		nBody, payload := getBodyPayload(req.Body, bufferSize)
		req.Body = nBody
		return payload
	}
	rqBody, rqErr := req.GetBody()
	if rqErr != nil {
		return ""
	}
	rqBodyBuffer := make([]byte, bufferSize)
	if ln, err := rqBody.Read(rqBodyBuffer); err == nil && ln > 0 {
		if ln < bufferSize {
			rqBodyBuffer = rqBodyBuffer[:ln]
		}
		return string(bytes.Runes(rqBodyBuffer))
	}
	return ""
}

// Gets the payload from a body
func getBodyPayload(body io.ReadCloser, bufferSize int) (io.ReadCloser, string) {
	if body == nil {
		return body, ""
	}
	rsBodyBuffer := make([]byte, bufferSize)
	ln, _ := body.Read(rsBodyBuffer)
	if ln == 0 {
		return body, ""
	}
	if ln < bufferSize {
		rsBodyBuffer = rsBodyBuffer[:ln]
	}
	rsPayload := string(bytes.Runes(rsBodyBuffer))
	rBody := struct {
		io.Reader
		io.Closer
	}{
		io.MultiReader(bytes.NewReader(rsBodyBuffer), body),
		body,
	}
	return rBody, rsPayload
}

// Gets the response payload
func getResponsePayload(resp *http.Response, bufferSize int) string {
	if resp == nil || resp.Body == nil || resp.Body == http.NoBody {
		return ""
	}
	rsBodyBuffer := make([]byte, bufferSize)
	ln, _ := resp.Body.Read(rsBodyBuffer)
	if ln == 0 {
		return ""
	}
	if ln < bufferSize {
		rsBodyBuffer = rsBodyBuffer[:ln]
	}
	rsPayload := string(bytes.Runes(rsBodyBuffer))
	resp.Body = struct {
		io.Reader
		io.Closer
	}{
		io.MultiReader(bytes.NewReader(rsBodyBuffer), resp.Body),
		resp.Body,
	}
	return rsPayload
}

// Tracer holds tracing details for one HTTP request.
type Tracer struct {
	tr   opentracing.Tracer
	root opentracing.Span
	sp   opentracing.Span
	opts *clientOptions
}

func (h *Tracer) start(req *http.Request) opentracing.Span {
	if h.root == nil {
		parent := opentracing.SpanFromContext(req.Context())
		h.root = parent
	}

	ctx := h.root.Context()
	h.sp = h.tr.StartSpan("HTTP "+req.Method, opentracing.ChildOf(ctx))
	ext.SpanKindRPCClient.Set(h.sp)

	componentName := h.opts.componentName
	if componentName == "" {
		componentName = defaultComponentName
	}
	ext.Component.Set(h.sp, componentName)

	return h.sp
}

// Finish finishes the span of the traced request.
func (h *Tracer) Finish() {
	if h.root != nil {
		h.root.Finish()
	}
}

// Span returns the root span of the traced request. This function
// should only be called after the request has been executed.
func (h *Tracer) Span() opentracing.Span {
	return h.root
}

func (h *Tracer) clientTrace() *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		GetConn:              h.getConn,
		GotConn:              h.gotConn,
		PutIdleConn:          h.putIdleConn,
		GotFirstResponseByte: h.gotFirstResponseByte,
		Got100Continue:       h.got100Continue,
		DNSStart:             h.dnsStart,
		DNSDone:              h.dnsDone,
		ConnectStart:         h.connectStart,
		ConnectDone:          h.connectDone,
		WroteHeaders:         h.wroteHeaders,
		Wait100Continue:      h.wait100Continue,
		WroteRequest:         h.wroteRequest,
	}
}

func (h *Tracer) getConn(hostPort string) {
}

func (h *Tracer) gotConn(info httptrace.GotConnInfo) {
	h.sp.SetTag("net/http.reused", info.Reused)
	h.sp.SetTag("net/http.was_idle", info.WasIdle)
}

func (h *Tracer) putIdleConn(error) {
}

func (h *Tracer) gotFirstResponseByte() {
}

func (h *Tracer) got100Continue() {
}

func (h *Tracer) dnsStart(info httptrace.DNSStartInfo) {
	ext.PeerHostname.Set(h.sp, info.Host)
}

func (h *Tracer) dnsDone(info httptrace.DNSDoneInfo) {
}

func (h *Tracer) connectStart(network, addr string) {
	ext.PeerAddress.Set(h.sp, addr)
	if idx := strings.IndexByte(addr, ':'); idx > -1 {
		ip := net.ParseIP(addr[:idx])
		if ip.Equal(ip.To4()) {
			ext.PeerHostIPv4.SetString(h.sp, ip.String())
		} else if ip.Equal(ip.To16()) {
			ext.PeerHostIPv6.Set(h.sp, ip.String())
		}
		if val, err := strconv.ParseUint(addr[idx+1:], 10, 16); err == nil {
			ext.PeerPort.Set(h.sp, uint16(val))
		}
	}
}

func (h *Tracer) connectDone(network, addr string, err error) {
	if err != nil {
		h.sp.LogFields(
			log.String("message", "ConnectDone"),
			log.String("network", network),
			log.String("addr", addr),
			log.String("event", "error"),
			log.Error(err),
		)
	}
}

func (h *Tracer) wroteHeaders() {
}

func (h *Tracer) wait100Continue() {
}

func (h *Tracer) wroteRequest(info httptrace.WroteRequestInfo) {
	if info.Err != nil {
		h.sp.LogFields(
			log.String("message", "WroteRequest"),
			log.String("event", "error"),
			log.Error(info.Err),
		)
		ext.Error.Set(h.sp, true)
	}
}
