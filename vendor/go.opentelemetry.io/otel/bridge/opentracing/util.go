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

package opentracing // import "go.opentelemetry.io/otel/bridge/opentracing"

import (
	"context"

	"go.opentelemetry.io/otel/trace"
)

// NewTracerPair is a utility function that creates a BridgeTracer and a
// WrapperTracerProvider. WrapperTracerProvider creates a single instance of
// WrapperTracer. The BridgeTracer forwards the calls to the WrapperTracer
// that wraps the passed tracer. BridgeTracer and WrapperTracerProvider are
// returned to the caller and the caller is expected to register BridgeTracer
// with opentracing and WrapperTracerProvider with opentelemetry.
func NewTracerPair(tracer trace.Tracer) (*BridgeTracer, *WrapperTracerProvider) {
	bridgeTracer := NewBridgeTracer()
	wrapperProvider := NewWrappedTracerProvider(bridgeTracer, tracer)
	bridgeTracer.SetOpenTelemetryTracer(wrapperProvider.Tracer(""))
	return bridgeTracer, wrapperProvider
}

func NewTracerPairWithContext(ctx context.Context, tracer trace.Tracer) (context.Context, *BridgeTracer, *WrapperTracerProvider) {
	bridgeTracer, wrapperProvider := NewTracerPair(tracer)
	ctx = bridgeTracer.NewHookedContext(ctx)
	return ctx, bridgeTracer, wrapperProvider
}
