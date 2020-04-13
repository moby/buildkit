package logging

import "github.com/opentracing/opentracing-go"

type logRecorder interface {
	Reset()
	GetRecords() []opentracing.LogRecord
}

var recorders []logRecorder

//
// We are doing like this because there is no way to call span.LogFields with a custom timestamp on each event.
// The only way is to create an opentracing.LogRecord array and call later:
//  span.FinishWithOptions(opentracing.FinishOptions{
//		LogRecords: logRecords,
//	}
//

// Start record in all registered writers (used by the StartTest in order to generate new records for the span)
func Reset() {
	for _, writer := range recorders {
		writer.Reset()
	}
}

// Stop record all registered writers (used by End in order to retrieve the records from the log and insert them in the span)
func GetRecords() []opentracing.LogRecord {
	var records []opentracing.LogRecord
	for _, writer := range recorders {
		records = append(records, writer.GetRecords()...)
	}
	return records
}
