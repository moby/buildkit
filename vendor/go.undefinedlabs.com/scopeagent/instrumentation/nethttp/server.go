package nethttp

import (
	"bytes"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/opentracing/opentracing-go"
	"github.com/opentracing/opentracing-go/ext"

	"go.undefinedlabs.com/scopeagent/env"
	"go.undefinedlabs.com/scopeagent/errors"
	"go.undefinedlabs.com/scopeagent/instrumentation"
)

type mwOptions struct {
	opNameFunc             func(r *http.Request) string
	spanFilter             func(r *http.Request) bool
	spanObserver           func(span opentracing.Span, r *http.Request)
	urlTagFunc             func(u *url.URL) string
	componentName          string
	payloadInstrumentation bool
}

// MWOption controls the behavior of the Middleware.
type MWOption func(*mwOptions)

// OperationNameFunc returns a MWOption that uses given function f
// to generate operation name for each server-side span.
func OperationNameFunc(f func(r *http.Request) string) MWOption {
	return func(options *mwOptions) {
		options.opNameFunc = f
	}
}

// MWComponentName returns a MWOption that sets the component name
// for the server-side span.
func MWComponentName(componentName string) MWOption {
	return func(options *mwOptions) {
		options.componentName = componentName
	}
}

// MWSpanFilter returns a MWOption that filters requests from creating a span
// for the server-side span.
// Span won't be created if it returns false.
func MWSpanFilter(f func(r *http.Request) bool) MWOption {
	return func(options *mwOptions) {
		options.spanFilter = f
	}
}

// MWSpanObserver returns a MWOption that observe the span
// for the server-side span.
func MWSpanObserver(f func(span opentracing.Span, r *http.Request)) MWOption {
	return func(options *mwOptions) {
		options.spanObserver = f
	}
}

// MWURLTagFunc returns a MWOption that uses given function f
// to set the span's http.url tag. Can be used to change the default
// http.url tag, eg to redact sensitive information.
func MWURLTagFunc(f func(u *url.URL) string) MWOption {
	return func(options *mwOptions) {
		options.urlTagFunc = f
	}
}

// Enable payload instrumentation
func MWPayloadInstrumentation() MWOption {
	return func(options *mwOptions) {
		options.payloadInstrumentation = true
	}
}

// Middleware wraps an http.Handler and traces incoming requests.
// Additionally, it adds the span to the request's context.
//
// By default, the operation name of the spans is set to "HTTP {method}".
// This can be overriden with options.
//
// Example:
// 	 http.ListenAndServe("localhost:80", nethttp.Middleware(tracer, http.DefaultServeMux))
//
// The options allow fine tuning the behavior of the middleware.
//
// Example:
//   mw := nethttp.Middleware(
//      tracer,
//      http.DefaultServeMux,
//      nethttp.OperationNameFunc(func(r *http.Request) string {
//	        return "HTTP " + r.Method + ":/api/customers"
//      }),
//      nethttp.MWSpanObserver(func(sp opentracing.Span, r *http.Request) {
//			sp.SetTag("http.uri", r.URL.EscapedPath())
//		}),
//   )
func middleware(tr opentracing.Tracer, h http.Handler, options ...MWOption) http.Handler {
	return middlewareFunc(tr, h.ServeHTTP, options...)
}

// MiddlewareFunc wraps an http.HandlerFunc and traces incoming requests.
// It behaves identically to the Middleware function above.
//
// Example:
//   http.ListenAndServe("localhost:80", nethttp.MiddlewareFunc(tracer, MyHandler))
func middlewareFunc(tr opentracing.Tracer, h http.HandlerFunc, options ...MWOption) http.HandlerFunc {
	opts := mwOptions{
		opNameFunc: func(r *http.Request) string {
			return "HTTP " + r.Method
		},
		spanFilter:   func(r *http.Request) bool { return true },
		spanObserver: func(span opentracing.Span, r *http.Request) {},
		urlTagFunc: func(u *url.URL) string {
			return u.String()
		},
	}
	for _, opt := range options {
		opt(&opts)
	}
	opts.payloadInstrumentation = opts.payloadInstrumentation || env.ScopeInstrumentationHttpPayloads.Value
	fn := func(w http.ResponseWriter, r *http.Request) {
		if !opts.spanFilter(r) {
			h(w, r)
			return
		}
		ctx, _ := tr.Extract(opentracing.HTTPHeaders, opentracing.HTTPHeadersCarrier(r.Header))
		sp := tr.StartSpan(opts.opNameFunc(r), ext.RPCServerOption(ctx))
		ext.HTTPMethod.Set(sp, r.Method)
		ext.HTTPUrl.Set(sp, opts.urlTagFunc(r.URL))
		opts.spanObserver(sp, r)

		// set component name, use "net/http" if caller does not specify
		componentName := opts.componentName
		if componentName == "" {
			componentName = defaultComponentName
		}
		ext.Component.Set(sp, componentName)

		ext.PeerAddress.Set(sp, r.RemoteAddr)
		ext.PeerHostIPv4.SetString(sp, r.RemoteAddr)
		if idx := strings.IndexByte(r.RemoteAddr, ':'); idx > -1 {
			ip := net.ParseIP(r.RemoteAddr[:idx])
			if ip.Equal(ip.To4()) {
				ext.PeerHostIPv4.SetString(sp, ip.String())
			} else if ip.Equal(ip.To16()) {
				ext.PeerHostIPv6.Set(sp, ip.String())
			}
			if val, err := strconv.ParseUint(r.RemoteAddr[idx+1:], 10, 16); err == nil {
				ext.PeerPort.Set(sp, uint16(val))
			}
		}

		rtracker := &responseTracker{ResponseWriter: w}
		rtracker.payloadInstrumentation = opts.payloadInstrumentation
		r = r.WithContext(opentracing.ContextWithSpan(r.Context(), sp))

		defer func() {
			ext.HTTPStatusCode.Set(sp, uint16(rtracker.status))
			if rtracker.status >= http.StatusBadRequest || !rtracker.wroteheader {
				ext.Error.Set(sp, true)
			}

			if rtracker.payloadInstrumentation {
				rqPayload := getRequestPayload(r, payloadBufferSize)
				sp.SetTag("http.request_payload", rqPayload)
			} else {
				sp.SetTag("http.request_payload.unavailable", "disabled")
			}

			if rtracker.payloadInstrumentation {
				rsRunes := bytes.Runes(rtracker.payloadBuffer)
				rsPayload := string(rsRunes)
				sp.SetTag("http.response_payload", rsPayload)
			} else {
				sp.SetTag("http.response_payload.unavailable", "disabled")
			}

			if r := recover(); r != nil {
				errors.WriteExceptionEvent(sp, r, 1)
				sp.Finish()
				panic(r)
			}

			sp.Finish()
		}()

		h(rtracker.wrappedResponseWriter(), r)
	}
	return http.HandlerFunc(fn)
}

func Middleware(h http.Handler, options ...MWOption) http.Handler {
	if h == nil {
		h = http.DefaultServeMux
	}
	return MiddlewareFunc(h.ServeHTTP, options...)
}

func MiddlewareFunc(h http.HandlerFunc, options ...MWOption) http.Handler {
	// Only trace requests that are part of a test trace
	options = append(options, MWSpanFilter(func(r *http.Request) bool {
		ctx, err := instrumentation.Tracer().Extract(opentracing.HTTPHeaders, opentracing.HTTPHeadersCarrier(r.Header))
		if err != nil {
			return false
		}
		inTest := false
		ctx.ForeachBaggageItem(func(k, v string) bool {
			if k == "trace.kind" && v == "test" {
				inTest = true
				return false
			}
			return true
		})
		return inTest
	}))
	return middlewareFunc(instrumentation.Tracer(), h, options...)
}
