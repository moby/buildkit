package tracer

import (
	"fmt"
	"github.com/opentracing/opentracing-go"
	"os"
	"strconv"
	"strings"
)

const (
	environmentKeyPrefix      = "CTX_"
	EnvironmentVariableFormat = 10
)

type envVarPropagator struct {
	tracer *tracerImpl
}

func (p *envVarPropagator) Inject(
	spanContext opentracing.SpanContext,
	opaqueCarrier interface{},
) error {
	sc, ok := spanContext.(SpanContext)
	if !ok {
		return opentracing.ErrInvalidSpanContext
	}
	opaqueValue := opaqueCarrier.(*[]string)
	carrier := envVarCarrier(*opaqueValue)
	if carrier == nil {
		return opentracing.ErrInvalidCarrier
	}
	carrier.Set(fieldNameTraceID, strconv.FormatUint(sc.TraceID, 16))
	carrier.Set(fieldNameSpanID, strconv.FormatUint(sc.SpanID, 16))
	carrier.Set(fieldNameSampled, strconv.FormatBool(sc.Sampled))
	for k, v := range sc.Baggage {
		carrier.Set(prefixBaggage+k, v)
	}
	appendScopeEnvVars(&carrier)
	*opaqueValue = carrier
	return nil
}

func (p *envVarPropagator) Extract(
	opaqueCarrier interface{},
) (opentracing.SpanContext, error) {
	opaqueValue := opaqueCarrier.(*[]string)
	carrier := envVarCarrier(*opaqueValue)
	if carrier == nil {
		return nil, opentracing.ErrInvalidCarrier
	}
	requiredFieldCount := 0
	var traceID, spanID uint64
	var sampled bool
	var err error
	decodedBaggage := make(map[string]string)
	err = carrier.ForeachKey(func(k, v string) error {
		switch strings.ToLower(k) {
		case fieldNameTraceID:
			traceID, err = strconv.ParseUint(v, 16, 64)
			if err != nil {
				return opentracing.ErrSpanContextCorrupted
			}
		case fieldNameSpanID:
			spanID, err = strconv.ParseUint(v, 16, 64)
			if err != nil {
				return opentracing.ErrSpanContextCorrupted
			}
		case fieldNameSampled:
			sampled, err = strconv.ParseBool(v)
			if err != nil {
				return opentracing.ErrSpanContextCorrupted
			}
		default:
			lowercaseK := strings.ToLower(k)
			if strings.HasPrefix(lowercaseK, prefixBaggage) {
				decodedBaggage[strings.TrimPrefix(lowercaseK, prefixBaggage)] = v
			}
			// Balance off the requiredFieldCount++ just below...
			requiredFieldCount--
		}
		requiredFieldCount++
		return nil
	})
	if err != nil {
		return nil, err
	}
	if requiredFieldCount < tracerStateFieldCount {
		if requiredFieldCount == 0 {
			return nil, opentracing.ErrSpanContextNotFound
		}
		return nil, opentracing.ErrSpanContextCorrupted
	}

	return SpanContext{
		TraceID: traceID,
		SpanID:  spanID,
		Sampled: sampled,
		Baggage: decodedBaggage,
	}, nil
}

// Environment variable names used by the utilities in the Shell and Utilities volume of IEEE Std 1003.1-2001
// consist solely of uppercase letters, digits, and the '_' (underscore)
var escapeMap = map[string]string{
	".": "__dt__",
	"-": "__dh__",
}

// Environment Variables Carrier
type envVarCarrier []string

// Set implements Set() of opentracing.TextMapWriter
func (carrier *envVarCarrier) Set(key, val string) {
	var newCarrier []string
	keyUpper := strings.ToUpper(key)
	ctxKey := escape(environmentKeyPrefix + keyUpper)
	if carrier != nil {
		for _, item := range *carrier {
			if strings.Index(item, ctxKey) < 0 {
				newCarrier = append(newCarrier, item)
			}
		}
	}
	newCarrier = append(newCarrier, fmt.Sprintf("%s=%s", ctxKey, val))
	*carrier = newCarrier
}

// ForeachKey conforms to the TextMapReader interface.
func (carrier *envVarCarrier) ForeachKey(handler func(key, val string) error) error {
	if carrier != nil {
		for _, item := range *carrier {
			if strings.Index(item, environmentKeyPrefix) >= 0 {
				kv := strings.Split(item, "=")
				err := handler(unescape(kv[0][4:]), kv[1])
				if err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// We need to sanitize the env vars due:
// Environment variable names used by the utilities in the Shell and Utilities volume of IEEE Std 1003.1-2001
// consist solely of uppercase letters, digits, and the '_' (underscore)
func escape(value string) string {
	for key, val := range escapeMap {
		value = strings.Replace(value, key, val, -1)
	}
	return value
}
func unescape(value string) string {
	for key, val := range escapeMap {
		value = strings.Replace(value, val, key, -1)
	}
	return value
}

// Append all SCOPE_* environment variables to a child command
func appendScopeEnvVars(carrier *envVarCarrier) {
	envVars := os.Environ()
	for _, item := range envVars {
		if strings.Index(item, "SCOPE_") == 0 {
			kv := strings.Split(item, "=")
			carrier.Set(kv[0], kv[1])
		}
	}
}
