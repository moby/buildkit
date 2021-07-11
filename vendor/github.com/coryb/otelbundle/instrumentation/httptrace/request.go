package httptrace

import (
	"crypto/tls"
	"net/http"
	"net/http/httptrace"
	"strconv"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

type clientTraceTransport struct {
	transport http.RoundTripper
}

var _ http.RoundTripper = (*clientTraceTransport)(nil)

func (t *clientTraceTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return t.transport.RoundTrip(RequestWithClientTrace(req))
}

// Transport creates an http transport that will automatically trace
// all requests if there is an OpenTelemetry trace.Span previously registered
// in the request Context.
func Transport(rt http.RoundTripper) http.RoundTripper {
	if rt == nil {
		rt = http.DefaultTransport
	}
	return &clientTraceTransport{
		transport: rt,
	}
}

// RequestWithClientTrace adds httptrace.ClientTrace instrumentation into the
// request if there is an OpenTelemetry trace.Span previously registered in the
// request Context.  This is modeled after the Zipkin transport tracing:
// https://github.com/openzipkin/zipkin-go/blob/v0.2.5/middleware/http/transport.go#L165
func RequestWithClientTrace(req *http.Request) *http.Request {
	ctx := req.Context()
	s := trace.SpanFromContext(ctx)
	if !s.SpanContext().IsValid() && s.IsRecording() {
		return req
	}

	return req.WithContext(
		httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
			GetConn: func(hostPort string) {
				s.AddEvent("Connecting")
				s.SetAttributes(addTags(tag("httptrace.get_connection.host_port", hostPort))...)
			},
			GotConn: func(info httptrace.GotConnInfo) {
				s.AddEvent("Connected")
				s.SetAttributes(addTags(
					tag("httptrace.got_connection.reused", strconv.FormatBool(info.Reused)),
					tag("httptrace.got_connection.was_idle", strconv.FormatBool(info.WasIdle)),
					maybeTag(info.WasIdle, "httptrace.got_connection.idle_time", info.IdleTime.String()),
				)...)
			},
			PutIdleConn: func(err error) {
				s.AddEvent("Put Idle Connection")
				s.SetAttributes(addTags(
					errTag("httptrace.put_idle_connection.error", err),
				)...)
			},
			GotFirstResponseByte: func() {
				s.AddEvent("First Response Byte")
			},
			Got100Continue: func() {
				s.AddEvent("Got 100 Continue")
			},
			DNSStart: func(info httptrace.DNSStartInfo) {
				s.AddEvent("DNS Start")
				s.SetAttributes(addTags(
					tag("httptrace.dns_start.host", info.Host),
				)...)
			},
			DNSDone: func(info httptrace.DNSDoneInfo) {
				var addrs []string
				for _, addr := range info.Addrs {
					addrs = append(addrs, addr.String())
				}
				s.AddEvent("DNS Done")
				s.SetAttributes(addTags(
					tag("httptrace.dns_done.addrs", strings.Join(addrs, " , ")),
					errTag("httptrace.dns_done.error", info.Err),
				)...)
			},
			ConnectStart: func(network, addr string) {
				s.AddEvent("Connect Start")
				s.SetAttributes(addTags(
					tag("httptrace.connect_start.network", network),
					tag("httptrace.connect_start.addr", addr),
				)...)
			},
			ConnectDone: func(network, addr string, err error) {
				s.AddEvent("Connect Done")
				s.SetAttributes(addTags(
					tag("httptrace.connect_done.network", network),
					tag("httptrace.connect_done.addr", addr),
					errTag("httptrace.connect_done.error", err),
				)...)
			},
			TLSHandshakeStart: func() {
				s.AddEvent("TLS Handshake Start")
			},
			TLSHandshakeDone: func(_ tls.ConnectionState, err error) {
				s.AddEvent("TLS Handshake Done")
				s.SetAttributes(addTags(
					errTag("httptrace.tls_handshake_done.error", err),
				)...)
			},
			WroteHeaders: func() {
				s.AddEvent("Wrote Headers")
			},
			Wait100Continue: func() {
				s.AddEvent("Wait 100 Continue")
			},
			WroteRequest: func(info httptrace.WroteRequestInfo) {
				s.AddEvent("Wrote Request")
				s.SetAttributes(addTags(
					errTag("httptrace.wrote_request.error", info.Err),
				)...)
			},
		}),
	)
}

func addTags(tags ...*attribute.KeyValue) []attribute.KeyValue {
	attrs := []attribute.KeyValue{}
	for _, tag := range tags {
		if tag != nil {
			attrs = append(attrs, *tag)
		}
	}
	return attrs
}

func tag(k, v string) *attribute.KeyValue {
	a := attribute.String(k, v)
	return &a
}

func errTag(k string, e error) *attribute.KeyValue {
	if e == nil {
		return nil
	}
	return tag(k, e.Error())
}

func maybeTag(t bool, k, v string) *attribute.KeyValue {
	if !t {
		return nil
	}
	return tag(k, v)
}
