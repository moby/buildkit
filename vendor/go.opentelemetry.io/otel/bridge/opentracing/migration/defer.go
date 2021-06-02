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

package migration // import "go.opentelemetry.io/otel/bridge/opentracing/migration"

import (
	"context"
)

type doDeferredContextSetupType struct{}

// WithDeferredSetup returns a context that can tell the OpenTelemetry
// tracer to skip the context setup in the Start() function.
func WithDeferredSetup(ctx context.Context) context.Context {
	return context.WithValue(ctx, doDeferredContextSetupType{}, doDeferredContextSetupType{})
}

// SkipContextSetup can tell the OpenTelemetry tracer to skip the
// context setup during the span creation in the Start() function.
func SkipContextSetup(ctx context.Context) bool {
	_, ok := ctx.Value(doDeferredContextSetupType{}).(doDeferredContextSetupType)
	return ok
}
