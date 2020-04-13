package errors

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-errors/errors"
	"github.com/opentracing/opentracing-go"
	"github.com/opentracing/opentracing-go/log"

	"go.undefinedlabs.com/scopeagent/instrumentation"
	"go.undefinedlabs.com/scopeagent/tags"
	"go.undefinedlabs.com/scopeagent/tracer"
)

const (
	EventType      = "event"
	EventSource    = "source"
	EventMessage   = "message"
	EventStack     = "stack"
	EventException = "exception"
)

type StackFrames struct {
	File       string
	LineNumber int
	Name       string
	Package    string
}

var markSpanAsError = errors.New("")

// Write exception event in span using the recover data from panic
func WriteExceptionEvent(span opentracing.Span, recoverData interface{}, skipFrames int) {
	span.SetTag("error", true)
	if recoverData == markSpanAsError {
		return
	}
	var exceptionFields = getExceptionLogFields("error", recoverData, skipFrames+1)
	span.LogFields(exceptionFields...)
}

func WriteExceptionEventInRawSpan(rawSpan *tracer.RawSpan, err **errors.Error) {
	if rawSpan.Tags == nil {
		rawSpan.Tags = opentracing.Tags{}
	}
	rawSpan.Tags["error"] = true
	if *err != markSpanAsError {
		var exceptionFields = getExceptionLogFields("error", *err, 1)
		if rawSpan.Logs == nil {
			rawSpan.Logs = []opentracing.LogRecord{}
		}
		rawSpan.Logs = append(rawSpan.Logs, opentracing.LogRecord{
			Timestamp: time.Now(),
			Fields:    exceptionFields,
		})
		*err = markSpanAsError
	}
}

// Gets the current stack frames array
func getCurrentStackFrames(skip int) []StackFrames {
	skip = skip + 1
	err := errors.New(nil)
	errStack := err.StackFrames()
	nLength := len(errStack) - skip
	if nLength < 0 {
		return nil
	}
	stackFrames := make([]StackFrames, 0)
	for idx, frame := range errStack {
		if idx >= skip {
			stackFrames = append(stackFrames, StackFrames{
				File:       filepath.Clean(frame.File),
				LineNumber: frame.LineNumber,
				Name:       frame.Name,
				Package:    frame.Package,
			})
		}
	}
	return stackFrames
}

// Write log event with stacktrace in span using the recover data from panic
func LogPanic(ctx context.Context, recoverData interface{}, skipFrames int) {
	span := opentracing.SpanFromContext(ctx)
	if span == nil {
		return
	}
	var exceptionFields = getExceptionLogFields(tags.LogEvent, recoverData, skipFrames+1)
	exceptionFields = append(exceptionFields, log.String(tags.LogEventLevel, tags.LogLevel_ERROR))
	span.LogFields(exceptionFields...)
}

// Gets the current stacktrace
func GetCurrentStackTrace(skip int) map[string]interface{} {
	var exFrames []map[string]interface{}
	for _, frame := range getCurrentStackFrames(skip + 1) {
		exFrames = append(exFrames, map[string]interface{}{
			"name":   frame.Name,
			"module": frame.Package,
			"file":   frame.File,
			"line":   frame.LineNumber,
		})
	}
	return map[string]interface{}{
		"frames": exFrames,
	}
}

// Get the current error with the fixed stacktrace
func GetCurrentError(recoverData interface{}) *errors.Error {
	return errors.Wrap(recoverData, 1)
}

func getExceptionLogFields(eventType string, recoverData interface{}, skipFrames int) []log.Field {
	if recoverData != nil {
		err := errors.Wrap(recoverData, 2+skipFrames)
		errMessage := err.Error()
		errStack := err.StackFrames()
		exceptionData := getExceptionFrameData(errMessage, errStack)
		source := ""

		if errStack != nil && len(errStack) > 0 {
			sourceRoot := instrumentation.GetSourceRoot()
			for _, currentFrame := range errStack {
				dir := filepath.Dir(currentFrame.File)
				if strings.Index(dir, sourceRoot) != -1 {
					source = fmt.Sprintf("%s:%d", currentFrame.File, currentFrame.LineNumber)
					break
				}
			}
		}

		fields := make([]log.Field, 5)
		fields[0] = log.String(EventType, eventType)
		fields[1] = log.String(EventSource, source)
		fields[2] = log.String(EventMessage, errMessage)
		fields[3] = log.String(EventStack, getStringStack(err, errStack))
		fields[4] = log.Object(EventException, exceptionData)
		return fields
	}
	return nil
}

func getStringStack(err *errors.Error, errStack []errors.StackFrame) string {
	var frames []string
	for _, frame := range errStack {
		frames = append(frames, frame.String())
	}
	return fmt.Sprintf("[%s]: %s\n\n%s", err.TypeName(), err.Error(), strings.Join(frames, ""))
}

func getExceptionFrameData(errMessage string, errStack []errors.StackFrame) map[string]interface{} {
	var exFrames []map[string]interface{}
	for _, frame := range errStack {
		exFrames = append(exFrames, map[string]interface{}{
			"name":   frame.Name,
			"module": frame.Package,
			"file":   filepath.Clean(frame.File),
			"line":   frame.LineNumber,
		})
	}
	exStack := map[string]interface{}{
		"frames": exFrames,
	}
	return map[string]interface{}{
		"message":    errMessage,
		"stacktrace": exStack,
	}
}
