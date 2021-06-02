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
	"fmt"
	"net/http"
	"strings"
	"sync"

	ot "github.com/opentracing/opentracing-go"
	otext "github.com/opentracing/opentracing-go/ext"
	otlog "github.com/opentracing/opentracing-go/log"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/bridge/opentracing/migration"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/internal/baggage"
	"go.opentelemetry.io/otel/internal/trace/noop"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

type bridgeSpanContext struct {
	baggageItems    baggage.Map
	otelSpanContext trace.SpanContext
}

var _ ot.SpanContext = &bridgeSpanContext{}

func newBridgeSpanContext(otelSpanContext trace.SpanContext, parentOtSpanContext ot.SpanContext) *bridgeSpanContext {
	bCtx := &bridgeSpanContext{
		baggageItems:    baggage.NewEmptyMap(),
		otelSpanContext: otelSpanContext,
	}
	if parentOtSpanContext != nil {
		parentOtSpanContext.ForeachBaggageItem(func(key, value string) bool {
			bCtx.setBaggageItem(key, value)
			return true
		})
	}
	return bCtx
}

func (c *bridgeSpanContext) ForeachBaggageItem(handler func(k, v string) bool) {
	c.baggageItems.Foreach(func(kv attribute.KeyValue) bool {
		return handler(string(kv.Key), kv.Value.Emit())
	})
}

func (c *bridgeSpanContext) setBaggageItem(restrictedKey, value string) {
	crk := http.CanonicalHeaderKey(restrictedKey)
	c.baggageItems = c.baggageItems.Apply(baggage.MapUpdate{SingleKV: attribute.String(crk, value)})
}

func (c *bridgeSpanContext) baggageItem(restrictedKey string) string {
	crk := http.CanonicalHeaderKey(restrictedKey)
	val, _ := c.baggageItems.Value(attribute.Key(crk))
	return val.Emit()
}

type bridgeSpan struct {
	otelSpan          trace.Span
	ctx               *bridgeSpanContext
	tracer            *BridgeTracer
	skipDeferHook     bool
	extraBaggageItems map[string]string
}

var _ ot.Span = &bridgeSpan{}

func newBridgeSpan(otelSpan trace.Span, bridgeSC *bridgeSpanContext, tracer *BridgeTracer) *bridgeSpan {
	return &bridgeSpan{
		otelSpan:          otelSpan,
		ctx:               bridgeSC,
		tracer:            tracer,
		skipDeferHook:     false,
		extraBaggageItems: nil,
	}
}

func (s *bridgeSpan) Finish() {
	s.otelSpan.End()
}

func (s *bridgeSpan) FinishWithOptions(opts ot.FinishOptions) {
	var otelOpts []trace.SpanOption

	if !opts.FinishTime.IsZero() {
		otelOpts = append(otelOpts, trace.WithTimestamp(opts.FinishTime))
	}
	for _, record := range opts.LogRecords {
		s.logRecord(record)
	}
	for _, data := range opts.BulkLogData {
		s.logRecord(data.ToLogRecord())
	}
	s.otelSpan.End(otelOpts...)
}

func (s *bridgeSpan) logRecord(record ot.LogRecord) {
	s.otelSpan.AddEvent(
		"",
		trace.WithTimestamp(record.Timestamp),
		trace.WithAttributes(otLogFieldsToOTelLabels(record.Fields)...),
	)
}

func (s *bridgeSpan) Context() ot.SpanContext {
	return s.ctx
}

func (s *bridgeSpan) SetOperationName(operationName string) ot.Span {
	s.otelSpan.SetName(operationName)
	return s
}

// SetTag method adds a tag to the span.
//
// Note about the following value conversions:
// - int -> int64
// - uint -> string
// - int32 -> int64
// - uint32 -> int64
// - uint64 -> string
// - float32 -> float64
func (s *bridgeSpan) SetTag(key string, value interface{}) ot.Span {
	switch key {
	case string(otext.SpanKind):
		// TODO: Should we ignore it?
	case string(otext.Error):
		if b, ok := value.(bool); ok && b {
			s.otelSpan.SetStatus(codes.Error, "")
		}
	default:
		s.otelSpan.SetAttributes(otTagToOTelLabel(key, value))
	}
	return s
}

func (s *bridgeSpan) LogFields(fields ...otlog.Field) {
	s.otelSpan.AddEvent(
		"",
		trace.WithAttributes(otLogFieldsToOTelLabels(fields)...),
	)
}

type bridgeFieldEncoder struct {
	pairs []attribute.KeyValue
}

var _ otlog.Encoder = &bridgeFieldEncoder{}

func (e *bridgeFieldEncoder) EmitString(key, value string) {
	e.emitCommon(key, value)
}

func (e *bridgeFieldEncoder) EmitBool(key string, value bool) {
	e.emitCommon(key, value)
}

func (e *bridgeFieldEncoder) EmitInt(key string, value int) {
	e.emitCommon(key, value)
}

func (e *bridgeFieldEncoder) EmitInt32(key string, value int32) {
	e.emitCommon(key, value)
}

func (e *bridgeFieldEncoder) EmitInt64(key string, value int64) {
	e.emitCommon(key, value)
}

func (e *bridgeFieldEncoder) EmitUint32(key string, value uint32) {
	e.emitCommon(key, value)
}

func (e *bridgeFieldEncoder) EmitUint64(key string, value uint64) {
	e.emitCommon(key, value)
}

func (e *bridgeFieldEncoder) EmitFloat32(key string, value float32) {
	e.emitCommon(key, value)
}

func (e *bridgeFieldEncoder) EmitFloat64(key string, value float64) {
	e.emitCommon(key, value)
}

func (e *bridgeFieldEncoder) EmitObject(key string, value interface{}) {
	e.emitCommon(key, value)
}

func (e *bridgeFieldEncoder) EmitLazyLogger(value otlog.LazyLogger) {
	value(e)
}

func (e *bridgeFieldEncoder) emitCommon(key string, value interface{}) {
	e.pairs = append(e.pairs, otTagToOTelLabel(key, value))
}

func otLogFieldsToOTelLabels(fields []otlog.Field) []attribute.KeyValue {
	encoder := &bridgeFieldEncoder{}
	for _, field := range fields {
		field.Marshal(encoder)
	}
	return encoder.pairs
}

func (s *bridgeSpan) LogKV(alternatingKeyValues ...interface{}) {
	fields, err := otlog.InterleavedKVToFields(alternatingKeyValues...)
	if err != nil {
		return
	}
	s.LogFields(fields...)
}

func (s *bridgeSpan) SetBaggageItem(restrictedKey, value string) ot.Span {
	s.updateOTelContext(restrictedKey, value)
	s.setBaggageItemOnly(restrictedKey, value)
	return s
}

func (s *bridgeSpan) setBaggageItemOnly(restrictedKey, value string) {
	s.ctx.setBaggageItem(restrictedKey, value)
}

func (s *bridgeSpan) updateOTelContext(restrictedKey, value string) {
	if s.extraBaggageItems == nil {
		s.extraBaggageItems = make(map[string]string)
	}
	s.extraBaggageItems[restrictedKey] = value
}

func (s *bridgeSpan) BaggageItem(restrictedKey string) string {
	return s.ctx.baggageItem(restrictedKey)
}

func (s *bridgeSpan) Tracer() ot.Tracer {
	return s.tracer
}

func (s *bridgeSpan) LogEvent(event string) {
	s.LogEventWithPayload(event, nil)
}

func (s *bridgeSpan) LogEventWithPayload(event string, payload interface{}) {
	data := ot.LogData{
		Event:   event,
		Payload: payload,
	}
	s.Log(data)
}

func (s *bridgeSpan) Log(data ot.LogData) {
	record := data.ToLogRecord()
	s.LogFields(record.Fields...)
}

type bridgeSetTracer struct {
	isSet      bool
	otelTracer trace.Tracer

	warningHandler BridgeWarningHandler
	warnOnce       sync.Once
}

func (s *bridgeSetTracer) tracer() trace.Tracer {
	if !s.isSet {
		s.warnOnce.Do(func() {
			s.warningHandler("The OpenTelemetry tracer is not set, default no-op tracer is used! Call SetOpenTelemetryTracer to set it up.\n")
		})
	}
	return s.otelTracer
}

// BridgeWarningHandler is a type of handler that receives warnings
// from the BridgeTracer.
type BridgeWarningHandler func(msg string)

// BridgeTracer is an implementation of the OpenTracing tracer, which
// translates the calls to the OpenTracing API into OpenTelemetry
// counterparts and calls the underlying OpenTelemetry tracer.
type BridgeTracer struct {
	setTracer bridgeSetTracer

	warningHandler BridgeWarningHandler
	warnOnce       sync.Once

	propagator propagation.TextMapPropagator
}

var _ ot.Tracer = &BridgeTracer{}
var _ ot.TracerContextWithSpanExtension = &BridgeTracer{}

// NewBridgeTracer creates a new BridgeTracer. The new tracer forwards
// the calls to the OpenTelemetry Noop tracer, so it should be
// overridden with the SetOpenTelemetryTracer function. The warnings
// handler does nothing by default, so to override it use the
// SetWarningHandler function.
func NewBridgeTracer() *BridgeTracer {
	return &BridgeTracer{
		setTracer: bridgeSetTracer{
			otelTracer: noop.Tracer,
		},
		warningHandler: func(msg string) {},
		propagator:     nil,
	}
}

// SetWarningHandler overrides the warning handler.
func (t *BridgeTracer) SetWarningHandler(handler BridgeWarningHandler) {
	t.setTracer.warningHandler = handler
	t.warningHandler = handler
}

// SetOpenTelemetryTracer overrides the underlying OpenTelemetry
// tracer. The passed tracer should know how to operate in the
// environment that uses OpenTracing API.
func (t *BridgeTracer) SetOpenTelemetryTracer(tracer trace.Tracer) {
	t.setTracer.otelTracer = tracer
	t.setTracer.isSet = true
}

func (t *BridgeTracer) SetTextMapPropagator(propagator propagation.TextMapPropagator) {
	t.propagator = propagator
}

func (t *BridgeTracer) NewHookedContext(ctx context.Context) context.Context {
	ctx = baggage.ContextWithSetHook(ctx, t.baggageSetHook)
	ctx = baggage.ContextWithGetHook(ctx, t.baggageGetHook)
	return ctx
}

func (t *BridgeTracer) baggageSetHook(ctx context.Context) context.Context {
	span := ot.SpanFromContext(ctx)
	if span == nil {
		t.warningHandler("No active OpenTracing span, can not propagate the baggage items from OpenTelemetry context\n")
		return ctx
	}
	bSpan, ok := span.(*bridgeSpan)
	if !ok {
		t.warningHandler("Encountered a foreign OpenTracing span, will not propagate the baggage items from OpenTelemetry context\n")
		return ctx
	}
	// we clear the context only to avoid calling a get hook
	// during MapFromContext, but otherwise we don't change the
	// context, so we don't care about the old hooks.
	clearCtx, _, _ := baggage.ContextWithNoHooks(ctx)
	m := baggage.MapFromContext(clearCtx)
	m.Foreach(func(kv attribute.KeyValue) bool {
		bSpan.setBaggageItemOnly(string(kv.Key), kv.Value.Emit())
		return true
	})
	return ctx
}

func (t *BridgeTracer) baggageGetHook(ctx context.Context, m baggage.Map) baggage.Map {
	span := ot.SpanFromContext(ctx)
	if span == nil {
		t.warningHandler("No active OpenTracing span, can not propagate the baggage items from OpenTracing span context\n")
		return m
	}
	bSpan, ok := span.(*bridgeSpan)
	if !ok {
		t.warningHandler("Encountered a foreign OpenTracing span, will not propagate the baggage items from OpenTracing span context\n")
		return m
	}
	items := bSpan.extraBaggageItems
	if len(items) == 0 {
		return m
	}
	kv := make([]attribute.KeyValue, 0, len(items))
	for k, v := range items {
		kv = append(kv, attribute.String(k, v))
	}
	return m.Apply(baggage.MapUpdate{MultiKV: kv})
}

// StartSpan is a part of the implementation of the OpenTracing Tracer
// interface.
func (t *BridgeTracer) StartSpan(operationName string, opts ...ot.StartSpanOption) ot.Span {
	sso := ot.StartSpanOptions{}
	for _, opt := range opts {
		opt.Apply(&sso)
	}
	parentBridgeSC, links := otSpanReferencesToParentAndLinks(sso.References)
	attributes, kind, hadTrueErrorTag := otTagsToOTelAttributesKindAndError(sso.Tags)
	checkCtx := migration.WithDeferredSetup(context.Background())
	if parentBridgeSC != nil {
		checkCtx = trace.ContextWithRemoteSpanContext(checkCtx, parentBridgeSC.otelSpanContext)
	}
	checkCtx2, otelSpan := t.setTracer.tracer().Start(
		checkCtx,
		operationName,
		trace.WithAttributes(attributes...),
		trace.WithTimestamp(sso.StartTime),
		trace.WithLinks(links...),
		trace.WithSpanKind(kind),
	)
	if checkCtx != checkCtx2 {
		t.warnOnce.Do(func() {
			t.warningHandler("SDK should have deferred the context setup, see the documentation of go.opentelemetry.io/otel/bridge/opentracing/migration\n")
		})
	}
	if hadTrueErrorTag {
		otelSpan.SetStatus(codes.Error, "")
	}
	// One does not simply pass a concrete pointer to function
	// that takes some interface. In case of passing nil concrete
	// pointer, we get an interface with non-nil type (because the
	// pointer type is known) and a nil value. Which means
	// interface is not nil, but calling some interface function
	// on it will most likely result in nil pointer dereference.
	var otSpanContext ot.SpanContext
	if parentBridgeSC != nil {
		otSpanContext = parentBridgeSC
	}
	sctx := newBridgeSpanContext(otelSpan.SpanContext(), otSpanContext)
	span := newBridgeSpan(otelSpan, sctx, t)

	return span
}

// ContextWithBridgeSpan sets up the context with the passed
// OpenTelemetry span as the active OpenTracing span.
//
// This function should be used by the OpenTelemetry tracers that want
// to be aware how to operate in the environment using OpenTracing
// API.
func (t *BridgeTracer) ContextWithBridgeSpan(ctx context.Context, span trace.Span) context.Context {
	var otSpanContext ot.SpanContext
	if parentSpan := ot.SpanFromContext(ctx); parentSpan != nil {
		otSpanContext = parentSpan.Context()
	}
	bCtx := newBridgeSpanContext(span.SpanContext(), otSpanContext)
	bSpan := newBridgeSpan(span, bCtx, t)
	bSpan.skipDeferHook = true
	return ot.ContextWithSpan(ctx, bSpan)
}

// ContextWithSpanHook is an implementation of the OpenTracing tracer
// extension interface. It will call the DeferredContextSetupHook
// function on the tracer if it implements the
// DeferredContextSetupTracerExtension interface.
func (t *BridgeTracer) ContextWithSpanHook(ctx context.Context, span ot.Span) context.Context {
	bSpan, ok := span.(*bridgeSpan)
	if !ok {
		t.warningHandler("Encountered a foreign OpenTracing span, will not run a possible deferred context setup hook\n")
		return ctx
	}
	if bSpan.skipDeferHook {
		return ctx
	}
	if tracerWithExtension, ok := bSpan.tracer.setTracer.tracer().(migration.DeferredContextSetupTracerExtension); ok {
		ctx = tracerWithExtension.DeferredContextSetupHook(ctx, bSpan.otelSpan)
	}
	return ctx
}

func otTagsToOTelAttributesKindAndError(tags map[string]interface{}) ([]attribute.KeyValue, trace.SpanKind, bool) {
	kind := trace.SpanKindInternal
	err := false
	var pairs []attribute.KeyValue
	for k, v := range tags {
		switch k {
		case string(otext.SpanKind):
			if s, ok := v.(string); ok {
				switch strings.ToLower(s) {
				case "client":
					kind = trace.SpanKindClient
				case "server":
					kind = trace.SpanKindServer
				case "producer":
					kind = trace.SpanKindProducer
				case "consumer":
					kind = trace.SpanKindConsumer
				}
			}
		case string(otext.Error):
			if b, ok := v.(bool); ok && b {
				err = true
			}
		default:
			pairs = append(pairs, otTagToOTelLabel(k, v))
		}
	}
	return pairs, kind, err
}

// otTagToOTelLabel converts given key-value into attribute.KeyValue.
// Note that some conversions are not obvious:
// - int -> int64
// - uint -> string
// - int32 -> int64
// - uint32 -> int64
// - uint64 -> string
// - float32 -> float64
func otTagToOTelLabel(k string, v interface{}) attribute.KeyValue {
	key := otTagToOTelLabelKey(k)
	switch val := v.(type) {
	case bool:
		return key.Bool(val)
	case int64:
		return key.Int64(val)
	case uint64:
		return key.String(fmt.Sprintf("%d", val))
	case float64:
		return key.Float64(val)
	case int32:
		return key.Int64(int64(val))
	case uint32:
		return key.Int64(int64(val))
	case float32:
		return key.Float64(float64(val))
	case int:
		return key.Int(val)
	case uint:
		return key.String(fmt.Sprintf("%d", val))
	case string:
		return key.String(val)
	default:
		return key.String(fmt.Sprint(v))
	}
}

func otTagToOTelLabelKey(k string) attribute.Key {
	return attribute.Key(k)
}

func otSpanReferencesToParentAndLinks(references []ot.SpanReference) (*bridgeSpanContext, []trace.Link) {
	var (
		parent *bridgeSpanContext
		links  []trace.Link
	)
	for _, reference := range references {
		bridgeSC, ok := reference.ReferencedContext.(*bridgeSpanContext)
		if !ok {
			// We ignore foreign ot span contexts,
			// sorry. We have no way of getting any
			// TraceID and SpanID out of it for form a
			// OTel SpanContext for OTel Link. And
			// we can't make it a parent - it also needs a
			// valid OTel SpanContext.
			continue
		}
		if parent != nil {
			links = append(links, otSpanReferenceToOTelLink(bridgeSC, reference.Type))
		} else {
			if reference.Type == ot.ChildOfRef {
				parent = bridgeSC
			} else {
				links = append(links, otSpanReferenceToOTelLink(bridgeSC, reference.Type))
			}
		}
	}
	return parent, links
}

func otSpanReferenceToOTelLink(bridgeSC *bridgeSpanContext, refType ot.SpanReferenceType) trace.Link {
	return trace.Link{
		SpanContext: bridgeSC.otelSpanContext,
		Attributes:  otSpanReferenceTypeToOTelLinkAttributes(refType),
	}
}

func otSpanReferenceTypeToOTelLinkAttributes(refType ot.SpanReferenceType) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String("ot-span-reference-type", otSpanReferenceTypeToString(refType)),
	}
}

func otSpanReferenceTypeToString(refType ot.SpanReferenceType) string {
	switch refType {
	case ot.ChildOfRef:
		// "extra", because first child-of reference is used
		// as a parent, so this function isn't even called for
		// it.
		return "extra-child-of"
	case ot.FollowsFromRef:
		return "follows-from-ref"
	default:
		return fmt.Sprintf("unknown-%d", int(refType))
	}
}

// fakeSpan is just a holder of span context, nothing more. It's for
// propagators, so they can get the span context from Go context.
type fakeSpan struct {
	trace.Span
	sc trace.SpanContext
}

func (s fakeSpan) SpanContext() trace.SpanContext {
	return s.sc
}

// Inject is a part of the implementation of the OpenTracing Tracer
// interface.
//
// Currently only the HTTPHeaders format is supported.
func (t *BridgeTracer) Inject(sm ot.SpanContext, format interface{}, carrier interface{}) error {
	bridgeSC, ok := sm.(*bridgeSpanContext)
	if !ok {
		return ot.ErrInvalidSpanContext
	}
	if !bridgeSC.otelSpanContext.IsValid() {
		return ot.ErrInvalidSpanContext
	}
	if builtinFormat, ok := format.(ot.BuiltinFormat); !ok || builtinFormat != ot.HTTPHeaders {
		return ot.ErrUnsupportedFormat
	}
	hhcarrier, ok := carrier.(ot.HTTPHeadersCarrier)
	if !ok {
		return ot.ErrInvalidCarrier
	}
	header := http.Header(hhcarrier)
	fs := fakeSpan{
		Span: noop.Span,
		sc:   bridgeSC.otelSpanContext,
	}
	ctx := trace.ContextWithSpan(context.Background(), fs)
	ctx = baggage.ContextWithMap(ctx, bridgeSC.baggageItems)
	t.getPropagator().Inject(ctx, propagation.HeaderCarrier(header))
	return nil
}

// Extract is a part of the implementation of the OpenTracing Tracer
// interface.
//
// Currently only the HTTPHeaders format is supported.
func (t *BridgeTracer) Extract(format interface{}, carrier interface{}) (ot.SpanContext, error) {
	if builtinFormat, ok := format.(ot.BuiltinFormat); !ok || builtinFormat != ot.HTTPHeaders {
		return nil, ot.ErrUnsupportedFormat
	}
	hhcarrier, ok := carrier.(ot.HTTPHeadersCarrier)
	if !ok {
		return nil, ot.ErrInvalidCarrier
	}
	header := http.Header(hhcarrier)
	ctx := t.getPropagator().Extract(context.Background(), propagation.HeaderCarrier(header))
	baggage := baggage.MapFromContext(ctx)
	bridgeSC := &bridgeSpanContext{
		baggageItems:    baggage,
		otelSpanContext: trace.SpanContextFromContext(ctx),
	}
	if !bridgeSC.otelSpanContext.IsValid() {
		return nil, ot.ErrSpanContextNotFound
	}
	return bridgeSC, nil
}

func (t *BridgeTracer) getPropagator() propagation.TextMapPropagator {
	if t.propagator != nil {
		return t.propagator
	}
	return otel.GetTextMapPropagator()
}
