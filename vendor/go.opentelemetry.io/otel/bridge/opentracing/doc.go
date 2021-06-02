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

// Package opentracing implements a bridge that forwards OpenTracing API
// calls to the OpenTelemetry SDK.
//
// To use the bridge, first create an OpenTelemetry tracer of
// choice. Then use the NewTracerPair() function to create two tracers
// - one implementing OpenTracing API (BridgeTracer) and one that
// implements the OpenTelemetry API (WrapperTracer) and mostly
// forwards the calls to the OpenTelemetry tracer of choice, but does
// some extra steps to make the interaction between both APIs
// working. If the OpenTelemetry tracer of choice already knows how to
// cooperate with OpenTracing API through the OpenTracing bridge
// (explained in detail below), then it is fine to skip the
// WrapperTracer by calling the NewBridgeTracer() function to get the
// bridge tracer and then passing the chosen OpenTelemetry tracer to
// the SetOpenTelemetryTracer() function of the bridge tracer.
//
// Bridge tracer also allows the user to install a warning handler
// through the SetWarningHandler() function. The warning handler will
// be called when there is some misbehavior of the OpenTelemetry
// tracer with regard to the cooperation with the OpenTracing API.
//
// For an OpenTelemetry tracer to cooperate with OpenTracing API
// through the BridgeTracer, the OpenTelemetry tracer needs to
// (reasoning is below the list):
//
// 1. Return the same context it received in the Start() function if
// migration.SkipContextSetup() returns true.
//
// 2. Implement the migration.DeferredContextSetupTracerExtension
// interface. The implementation should setup the context it would
// normally do in the Start() function if the
// migration.SkipContextSetup() function returned false. Calling
// ContextWithBridgeSpan() is not necessary.
//
// 3. Have an access to the BridgeTracer instance.
//
// 4. If the migration.SkipContextSetup() function returned false, the
// tracer should use the ContextWithBridgeSpan() function to install the
// created span as an active OpenTracing span.
//
// There are some differences between OpenTracing and OpenTelemetry
// APIs, especially with regard to Go context handling. When a span is
// created with an OpenTracing API (through the StartSpan() function)
// the Go context is not available. BridgeTracer has access to the
// OpenTelemetry tracer of choice, so in the StartSpan() function
// BridgeTracer translates the parameters to the OpenTelemetry version
// and uses the OpenTelemetry tracer's Start() function to actually
// create a span. The OpenTelemetry Start() function takes the Go
// context as a parameter, so BridgeTracer at this point passes a
// temporary context to Start(). All the changes to the temporary
// context will be lost at the end of the StartSpan() function, so the
// OpenTelemetry tracer of choice should not do anything with the
// context. If the returned context is different, BridgeTracer will
// warn about it. The OpenTelemetry tracer of choice can learn about
// this situation by using the migration.SkipContextSetup()
// function. The tracer will receive an opportunity to set up the
// context at a later stage. Usually after StartSpan() is finished,
// users of the OpenTracing API are calling (either directly or
// through the opentracing.StartSpanFromContext() helper function) the
// opentracing.ContextWithSpan() function to insert the created
// OpenTracing span into the context. At that time, the OpenTelemetry
// tracer of choice has a chance of setting up the context through a
// hook invoked inside the opentracing.ContextWithSpan() function. For
// that to happen, the tracer should implement the
// migration.DeferredContextSetupTracerExtension interface. This so
// far explains the need for points 1. and 2.
//
// When the span is created with the OpenTelemetry API (with the
// Start() function) then migration.SkipContextSetup() will return
// false. This means that the tracer can do the usual setup of the
// context, but it also should set up the active OpenTracing span in
// the context. This is because OpenTracing API is not used at all in
// the creation of the span, but the OpenTracing API may be used
// during the time when the created OpenTelemetry span is current. For
// this case to work, we need to also set up active OpenTracing span
// in the context. This can be done with the ContextWithBridgeSpan()
// function. This means that the OpenTelemetry tracer of choice needs
// to have an access to the BridgeTracer instance. This should explain
// the need for points 3. and 4.
//
// Another difference related to the Go context handling is in logging
// - OpenTracing API does not take a context parameter in the
// LogFields() function, so when the call to the function gets
// translated to OpenTelemetry AddEvent() function, an empty context
// is passed.
package opentracing // import "go.opentelemetry.io/otel/bridge/opentracing"
