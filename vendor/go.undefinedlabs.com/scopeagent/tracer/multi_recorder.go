package tracer

type (
	multiSpanRecorder struct {
		recorders []SpanRecorder
	}
)

// Create a new multi recorder
func NewMultiRecorder(recorders ...SpanRecorder) SpanRecorder {
	return &multiSpanRecorder{
		recorders: recorders,
	}
}

func (r *multiSpanRecorder) RecordSpan(span RawSpan) {
	for idx := range r.recorders {
		r.recorders[idx].RecordSpan(span)
	}
}
