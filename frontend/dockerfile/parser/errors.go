package parser

import "github.com/pkg/errors"

// ErrorLocation gives a location in source code that caused the error
type ErrorLocation struct {
	Location []Range
	error
}

// Unwrap unwraps to the next error
func (e *ErrorLocation) Unwrap() error {
	return e.error
}

// Range is a code section between two positions
type Range struct {
	Start Position
	End   Position
}

// Position is a point in source code
type Position struct {
	Line      int
	Character int
}

func withLocation(err error, start, end int) error {
	return WithLocation(err, toRanges(start, end))
}

// WithLocation extends an error with a source code location
func WithLocation(err error, location []Range) error {
	if err == nil {
		return nil
	}
	var el *ErrorLocation
	if errors.As(err, &el) {
		return err
	}
	var err1 error = &ErrorLocation{
		error:    err,
		Location: location,
	}
	if !hasLocalStackTrace(err) {
		err1 = errors.WithStack(err1)
	}
	return err1
}

func toRanges(start, end int) (r []Range) {
	if end <= start {
		end = start
	}
	for i := start; i <= end; i++ {
		r = append(r, Range{Start: Position{Line: i}, End: Position{Line: i}})
	}
	return
}

func hasLocalStackTrace(err error) bool {
	wrapped, ok := err.(interface {
		Unwrap() error
	})
	if ok && hasLocalStackTrace(wrapped.Unwrap()) {
		return true
	}

	_, ok = err.(interface {
		StackTrace() errors.StackTrace
	})
	return ok
}
