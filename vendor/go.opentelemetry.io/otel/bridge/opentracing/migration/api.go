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

// Package migration provides interfaces and functions that are useful for
// providing a cooperation of the OpenTelemetry tracers with the
// OpenTracing API.
package migration // import "go.opentelemetry.io/otel/bridge/opentracing/migration"

import (
	"context"

	"go.opentelemetry.io/otel/trace"
)

// DeferredContextSetupTracerExtension is an interface an
// OpenTelemetry tracer may implement in order to cooperate with the
// calls to the OpenTracing API.
//
// Tracers implementing this interface should also use the
// SkipContextSetup() function during creation of the span in the
// Start() function to skip the configuration of the context.
type DeferredContextSetupTracerExtension interface {
	// DeferredContextSetupHook is called by the bridge
	// OpenTracing tracer when opentracing.ContextWithSpan is
	// called. This allows the OpenTelemetry tracer to set up the
	// context in a way it would normally do during the Start()
	// function. Since OpenTracing API does not support
	// configuration of the context during span creation, it needs
	// to be deferred until the call to the
	// opentracing.ContextWithSpan happens. When bridge
	// OpenTracing tracer calls OpenTelemetry tracer's Start()
	// function, it passes a context that shouldn't be modified.
	DeferredContextSetupHook(ctx context.Context, span trace.Span) context.Context
}

// OverrideTracerSpanExtension is an interface an OpenTelemetry span
// may implement in order to cooperate with the calls to the
// OpenTracing API.
//
// TODO(krnowak): I'm actually not so sold on the idea… The reason for
// introducing this interface was to have a span "created" by the
// WrapperTracer return WrapperTracer from the Tracer() function, not
// the real OpenTelemetry tracer that actually created the span. I'm
// thinking that I could create a wrapperSpan type that wraps an
// OpenTelemetry Span object and have WrapperTracer to alter the
// current OpenTelemetry span in the context so it points to the
// wrapped object, so the code in the tracer like
// `trace.SpanFromContent().(*realSpan)` would still work. Another
// argument for getting rid of this interface is that is only called
// by the WrapperTracer - WrapperTracer likely shouldn't require any
// changes in the underlying OpenTelemetry tracer to have things
// somewhat working.
//
// See the "tracer mess" test in mix_test.go.
type OverrideTracerSpanExtension interface {
	// OverrideTracer makes the span to return the passed tracer
	// from its Tracer() function.
	//
	// You don't need to implement this function if your
	// OpenTelemetry tracer cooperates well with the OpenTracing
	// API calls. In such case, there is no need to use the
	// WrapperTracer and thus no need to override the result of
	// the Tracer() function.
	OverrideTracer(tracer trace.Tracer)
}
