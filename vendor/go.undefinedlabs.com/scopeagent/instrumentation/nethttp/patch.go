package nethttp

import (
	"go.undefinedlabs.com/scopeagent/env"
	"net/http"
	"sync"
)

type Option func(*Transport)

var once sync.Once

// Enables the payload instrumentation in the transport
func WithPayloadInstrumentation() Option {
	return func(t *Transport) {
		t.PayloadInstrumentation = true
	}
}

// Enables stacktrace
func WithStacktrace() Option {
	return func(t *Transport) {
		t.Stacktrace = true
	}
}

// Patches the default http client with the instrumented transport
func PatchHttpDefaultClient(options ...Option) {
	once.Do(func() {
		transport := &Transport{RoundTripper: http.DefaultTransport}
		for _, option := range options {
			option(transport)
		}
		transport.PayloadInstrumentation = transport.PayloadInstrumentation || env.ScopeInstrumentationHttpPayloads.Value
		transport.Stacktrace = transport.Stacktrace || env.ScopeInstrumentationHttpStacktrace.Value
		http.DefaultClient = &http.Client{Transport: transport}
	})
}
