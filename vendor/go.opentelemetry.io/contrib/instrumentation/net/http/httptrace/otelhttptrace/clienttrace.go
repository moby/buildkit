// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package otelhttptrace // import "go.opentelemetry.io/contrib/instrumentation/net/http/httptrace/otelhttptrace"

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net/http/httptrace"
	"net/textproto"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	semconv "go.opentelemetry.io/otel/semconv/v1.20.0"
	"go.opentelemetry.io/otel/trace"
)

// ScopeName is the instrumentation scope name.
const ScopeName = "go.opentelemetry.io/otel/instrumentation/httptrace"

// HTTP attributes.
var (
	HTTPStatus                 = attribute.Key("http.status")
	HTTPHeaderMIME             = attribute.Key("http.mime")
	HTTPRemoteAddr             = attribute.Key("http.remote")
	HTTPLocalAddr              = attribute.Key("http.local")
	HTTPConnectionReused       = attribute.Key("http.conn.reused")
	HTTPConnectionWasIdle      = attribute.Key("http.conn.wasidle")
	HTTPConnectionIdleTime     = attribute.Key("http.conn.idletime")
	HTTPConnectionStartNetwork = attribute.Key("http.conn.start.network")
	HTTPConnectionDoneNetwork  = attribute.Key("http.conn.done.network")
	HTTPConnectionDoneAddr     = attribute.Key("http.conn.done.addr")
	HTTPDNSAddrs               = attribute.Key("http.dns.addrs")
)

var hookMap = map[string]string{
	"http.dns":     "http.getconn",
	"http.connect": "http.getconn",
	"http.tls":     "http.getconn",
}

func parentHook(hook string) string {
	if strings.HasPrefix(hook, "http.connect") {
		return hookMap["http.connect"]
	}
	return hookMap[hook]
}

// ClientTraceOption allows customizations to how the httptrace.Client
// collects information.
type ClientTraceOption interface {
	apply(*clientTracer)
}

type clientTraceOptionFunc func(*clientTracer)

func (fn clientTraceOptionFunc) apply(c *clientTracer) {
	fn(c)
}

// WithoutSubSpans will modify the httptrace.ClientTrace to only collect data
// as Events and Attributes on a span found in the context.  By default
// sub-spans will be generated.
func WithoutSubSpans() ClientTraceOption {
	return clientTraceOptionFunc(func(ct *clientTracer) {
		ct.useSpans = false
	})
}

// WithRedactedHeaders will be replaced by fixed '****' values for the header
// names provided.  These are in addition to the sensitive headers already
// redacted by default: Authorization, WWW-Authenticate, Proxy-Authenticate
// Proxy-Authorization, Cookie, Set-Cookie.
func WithRedactedHeaders(headers ...string) ClientTraceOption {
	return clientTraceOptionFunc(func(ct *clientTracer) {
		for _, header := range headers {
			ct.redactedHeaders[strings.ToLower(header)] = struct{}{}
		}
	})
}

// WithoutHeaders will disable adding span attributes for the http headers
// and values.
func WithoutHeaders() ClientTraceOption {
	return clientTraceOptionFunc(func(ct *clientTracer) {
		ct.addHeaders = false
	})
}

// WithInsecureHeaders will add span attributes for all http headers *INCLUDING*
// the sensitive headers that are redacted by default.  The attribute values
// will include the raw un-redacted text.  This might be useful for
// debugging authentication related issues, but should not be used for
// production deployments.
func WithInsecureHeaders() ClientTraceOption {
	return clientTraceOptionFunc(func(ct *clientTracer) {
		ct.addHeaders = true
		ct.redactedHeaders = nil
	})
}

// WithTracerProvider specifies a tracer provider for creating a tracer.
// The global provider is used if none is specified.
func WithTracerProvider(provider trace.TracerProvider) ClientTraceOption {
	return clientTraceOptionFunc(func(ct *clientTracer) {
		if provider != nil {
			ct.tracerProvider = provider
		}
	})
}

type Event struct {
	Time  time.Time
	Event string
	Attrs map[string]string
}

type EventLogger struct {
	tc map[*clientTracer]struct{}
}

type eventLoggerKey struct{}

func WithEventLogger(ctx context.Context) (context.Context, *EventLogger) {
	el := &EventLogger{}
	return context.WithValue(ctx, eventLoggerKey{}, el), el
}

func (el *EventLogger) LogAllEvents() {
	if len(el.tc) == 0 {
		log.Println("No ClientTraces found")
	}
	for ct := range el.tc {
		log.Printf("ClientTrace %p %T", ct, ct)
		for i, event := range ct.Events {
			log.Printf("event %d %v: %s", i, event.Time, event.Event)
			for k, v := range event.Attrs {
				log.Printf("  %s: %s", k, v)
			}
		}
	}
}

type clientTracer struct {
	context.Context

	tracerProvider trace.TracerProvider

	tr trace.Tracer

	activeHooks     map[string]context.Context
	root            trace.Span
	mtx             sync.Mutex
	redactedHeaders map[string]struct{}
	addHeaders      bool
	useSpans        bool

	mu     sync.Mutex
	Events []Event
}

// NewClientTrace returns an httptrace.ClientTrace implementation that will
// record OpenTelemetry spans for requests made by an http.Client. By default
// several spans will be added to the trace for various stages of a request
// (dns, connection, tls, etc). Also by default, all HTTP headers will be
// added as attributes to spans, although several headers will be automatically
// redacted: Authorization, WWW-Authenticate, Proxy-Authenticate,
// Proxy-Authorization, Cookie, and Set-Cookie.
func NewClientTrace(ctx context.Context, opts ...ClientTraceOption) *httptrace.ClientTrace {
	ct := &clientTracer{
		Context:     ctx,
		activeHooks: make(map[string]context.Context),
		redactedHeaders: map[string]struct{}{
			"authorization":       {},
			"www-authenticate":    {},
			"proxy-authenticate":  {},
			"proxy-authorization": {},
			"cookie":              {},
			"set-cookie":          {},
		},
		addHeaders: true,
		useSpans:   true,
	}

	if span := trace.SpanFromContext(ctx); span.SpanContext().IsValid() {
		ct.tracerProvider = span.TracerProvider()
	} else {
		ct.tracerProvider = otel.GetTracerProvider()
	}

	for _, opt := range opts {
		opt.apply(ct)
	}

	ct.tr = ct.tracerProvider.Tracer(
		ScopeName,
		trace.WithInstrumentationVersion(Version()),
	)

	if es, ok := ctx.Value(eventLoggerKey{}).(*EventLogger); ok {
		if es.tc == nil {
			es.tc = make(map[*clientTracer]struct{})
		}
		es.tc[ct] = struct{}{}
	}

	return &httptrace.ClientTrace{
		GetConn:              ct.getConn,
		GotConn:              ct.gotConn,
		PutIdleConn:          ct.putIdleConn,
		GotFirstResponseByte: ct.gotFirstResponseByte,
		Got100Continue:       ct.got100Continue,
		Got1xxResponse:       ct.got1xxResponse,
		DNSStart:             ct.dnsStart,
		DNSDone:              ct.dnsDone,
		ConnectStart:         ct.connectStart,
		ConnectDone:          ct.connectDone,
		TLSHandshakeStart:    ct.tlsHandshakeStart,
		TLSHandshakeDone:     ct.tlsHandshakeDone,
		WroteHeaderField:     ct.wroteHeaderField,
		WroteHeaders:         ct.wroteHeaders,
		Wait100Continue:      ct.wait100Continue,
		WroteRequest:         ct.wroteRequest,
	}
}

func (ct *clientTracer) logEvent(event string, attrs map[string]string) {
	ct.mu.Lock()
	defer ct.mu.Unlock()
	ct.Events = append(ct.Events, Event{
		Time:  time.Now(),
		Event: event,
		Attrs: attrs,
	})
}

func (ct *clientTracer) start(hook, spanName string, attrs ...attribute.KeyValue) {
	if !ct.useSpans {
		if ct.root == nil {
			ct.root = trace.SpanFromContext(ct.Context)
		}
		ct.root.AddEvent(hook+".start", trace.WithAttributes(attrs...))
		return
	}

	ct.mtx.Lock()
	defer ct.mtx.Unlock()

	if hookCtx, found := ct.activeHooks[hook]; !found {
		var sp trace.Span
		ct.activeHooks[hook], sp = ct.tr.Start(ct.getParentContext(hook), spanName, trace.WithAttributes(attrs...), trace.WithSpanKind(trace.SpanKindClient))
		if ct.root == nil {
			ct.root = sp
		}
	} else {
		// end was called before start finished, add the start attributes and end the span here
		span := trace.SpanFromContext(hookCtx)
		span.SetAttributes(attrs...)
		span.End()

		delete(ct.activeHooks, hook)
	}
}

func (ct *clientTracer) end(hook string, err error, attrs ...attribute.KeyValue) {
	if !ct.useSpans {
		// sometimes end may be called without previous start
		if ct.root == nil {
			ct.root = trace.SpanFromContext(ct.Context)
		}
		if err != nil {
			attrs = append(attrs, attribute.String(hook+".error", err.Error()))
		}
		ct.root.AddEvent(hook+".done", trace.WithAttributes(attrs...))
		return
	}

	ct.mtx.Lock()
	defer ct.mtx.Unlock()
	if ctx, ok := ct.activeHooks[hook]; ok {
		span := trace.SpanFromContext(ctx)
		if err != nil {
			span.SetStatus(codes.Error, err.Error())
		}
		span.SetAttributes(attrs...)
		span.End()
		delete(ct.activeHooks, hook)
	} else {
		// start is not finished before end is called.
		// Start a span here with the ending attributes that will be finished when start finishes.
		// Yes, it's backwards. v0v
		ctx, span := ct.tr.Start(ct.getParentContext(hook), hook, trace.WithAttributes(attrs...), trace.WithSpanKind(trace.SpanKindClient))
		if err != nil {
			span.SetStatus(codes.Error, err.Error())
		}
		ct.activeHooks[hook] = ctx
	}
}

func (ct *clientTracer) getParentContext(hook string) context.Context {
	ctx, ok := ct.activeHooks[parentHook(hook)]
	if !ok {
		return ct.Context
	}
	return ctx
}

func (ct *clientTracer) span(hook string) trace.Span {
	ct.mtx.Lock()
	defer ct.mtx.Unlock()
	if ctx, ok := ct.activeHooks[hook]; ok {
		return trace.SpanFromContext(ctx)
	}
	return nil
}

func (ct *clientTracer) getConn(host string) {
	ct.logEvent("http.getconn", map[string]string{
		"host": host,
	})
	ct.start("http.getconn", "http.getconn", semconv.NetHostName(host))
}

func (ct *clientTracer) gotConn(info httptrace.GotConnInfo) {
	ct.logEvent("http.gotconn", map[string]string{
		"conn.remote":   info.Conn.RemoteAddr().String(),
		"conn.local":    info.Conn.LocalAddr().String(),
		"conn.p":        fmt.Sprintf("%p", info.Conn),
		"conn.reused":   strconv.FormatBool(info.Reused),
		"conn.wasidle":  strconv.FormatBool(info.WasIdle),
		"conn.idletime": info.IdleTime.String(),
	})
	attrs := []attribute.KeyValue{
		HTTPRemoteAddr.String(info.Conn.RemoteAddr().String()),
		HTTPLocalAddr.String(info.Conn.LocalAddr().String()),
		HTTPConnectionReused.Bool(info.Reused),
		HTTPConnectionWasIdle.Bool(info.WasIdle),
	}
	if info.WasIdle {
		attrs = append(attrs, HTTPConnectionIdleTime.String(info.IdleTime.String()))
	}
	ct.end("http.getconn", nil, attrs...)
}

func (ct *clientTracer) putIdleConn(err error) {
	ct.logEvent("http.putidleconn", map[string]string{
		"error": fmt.Sprintf("%v", err),
	})
	ct.end("http.receive", err)
}

func (ct *clientTracer) gotFirstResponseByte() {
	ct.logEvent("http.gotfirstresponsebyte", nil)
	ct.start("http.receive", "http.receive")
}

func (ct *clientTracer) dnsStart(info httptrace.DNSStartInfo) {
	ct.logEvent("http.dnsStart", map[string]string{
		"host": info.Host,
	})
	ct.start("http.dns", "http.dns", semconv.NetHostName(info.Host))
}

func (ct *clientTracer) dnsDone(info httptrace.DNSDoneInfo) {
	ct.logEvent("http.dnsDone", map[string]string{
		"addrs":     fmt.Sprintf("%v", info.Addrs),
		"error":     fmt.Sprintf("%v", info.Err),
		"coalesced": strconv.FormatBool(info.Coalesced),
	})
	var addrs []string
	for _, netAddr := range info.Addrs {
		addrs = append(addrs, netAddr.String())
	}
	ct.end("http.dns", info.Err, HTTPDNSAddrs.String(sliceToString(addrs)))
}

func (ct *clientTracer) connectStart(network, addr string) {
	ct.logEvent("http.connectStart", map[string]string{
		"network": network,
		"addr":    addr,
	})
	ct.start("http.connect."+addr, "http.connect",
		HTTPRemoteAddr.String(addr),
		HTTPConnectionStartNetwork.String(network),
	)
}

func (ct *clientTracer) connectDone(network, addr string, err error) {
	ct.logEvent("http.connectDone", map[string]string{
		"network": network,
		"addr":    addr,
		"error":   fmt.Sprintf("%v", err),
	})
	ct.end("http.connect."+addr, err,
		HTTPConnectionDoneAddr.String(addr),
		HTTPConnectionDoneNetwork.String(network),
	)
}

func (ct *clientTracer) tlsHandshakeStart() {
	ct.logEvent("http.tlsHandshakeStart", nil)
	ct.start("http.tls", "http.tls")
}

func (ct *clientTracer) tlsHandshakeDone(st tls.ConnectionState, err error) {
	ct.logEvent("http.tlsHandshakeDone", map[string]string{
		"error":      fmt.Sprintf("%v", err),
		"version":    strconv.Itoa(int(st.Version)),
		"handshake":  strconv.FormatBool(st.HandshakeComplete),
		"didresume":  strconv.FormatBool(st.DidResume),
		"servername": st.ServerName,
	})
	ct.end("http.tls", err)
}

func (ct *clientTracer) wroteHeaderField(k string, v []string) {
	ct.logEvent("http.wroteHeaderField", map[string]string{
		"key": k,
	})
	if ct.useSpans && ct.span("http.headers") == nil {
		ct.start("http.headers", "http.headers")
	}
	if !ct.addHeaders {
		return
	}
	k = strings.ToLower(k)
	value := sliceToString(v)
	if _, ok := ct.redactedHeaders[k]; ok {
		value = "****"
	}
	ct.root.SetAttributes(attribute.String("http.request.header."+k, value))
}

func (ct *clientTracer) wroteHeaders() {
	ct.logEvent("http.wroteHeaders", nil)
	if ct.useSpans && ct.span("http.headers") != nil {
		ct.end("http.headers", nil)
	}
	ct.start("http.send", "http.send")
}

func (ct *clientTracer) wroteRequest(info httptrace.WroteRequestInfo) {
	ct.logEvent("http.wroteRequest", map[string]string{
		"error": fmt.Sprintf("%v", info.Err),
	})
	if info.Err != nil {
		ct.root.SetStatus(codes.Error, info.Err.Error())
	}
	ct.end("http.send", info.Err)
}

func (ct *clientTracer) got100Continue() {
	ct.logEvent("http.got100Continue", nil)
	span := ct.root
	if ct.useSpans {
		span = ct.span("http.receive")
	}
	span.AddEvent("GOT 100 - Continue")
}

func (ct *clientTracer) wait100Continue() {
	ct.logEvent("http.wait100Continue", nil)
	span := ct.root
	if ct.useSpans {
		span = ct.span("http.send")
	}
	span.AddEvent("GOT 100 - Wait")
}

func (ct *clientTracer) got1xxResponse(code int, header textproto.MIMEHeader) error {
	ct.logEvent("http.got1xxResponse", map[string]string{
		"code":   strconv.Itoa(code),
		"header": sm2s(header),
	})
	span := ct.root
	if ct.useSpans {
		span = ct.span("http.receive")
	}
	span.AddEvent("GOT 1xx", trace.WithAttributes(
		HTTPStatus.Int(code),
		HTTPHeaderMIME.String(sm2s(header)),
	))
	return nil
}

func sliceToString(value []string) string {
	if len(value) == 0 {
		return "undefined"
	}
	return strings.Join(value, ",")
}

func sm2s(value map[string][]string) string {
	var buf strings.Builder
	for k, v := range value {
		if buf.Len() != 0 {
			_, _ = buf.WriteString(",")
		}
		_, _ = buf.WriteString(k)
		_, _ = buf.WriteString("=")
		_, _ = buf.WriteString(sliceToString(v))
	}
	return buf.String()
}
